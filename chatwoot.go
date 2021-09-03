package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"

	_ "github.com/jackc/pgx/v4/stdlib"
	log "github.com/sirupsen/logrus"
	"maunium.net/go/mautrix"
	mcrypto "maunium.net/go/mautrix/crypto"
	mevent "maunium.net/go/mautrix/event"
	mid "maunium.net/go/mautrix/id"

	"gitlab.com/beeper/chatwoot/chatwootapi"
	"gitlab.com/beeper/chatwoot/store"
)

var client *mautrix.Client
var configuration Configuration
var olmMachine *mcrypto.OlmMachine
var stateStore *store.StateStore

var chatwootApi *chatwootapi.ChatwootAPI
var botHomeserver string

var userSendlocks map[mid.UserID]*sync.Mutex

var VERSION = "0.2.1"

func main() {
	// Arg parsing
	configPath := flag.String("config", "./config.json", "config file location")
	logLevelStr := flag.String("loglevel", "debug", "the log level")
	logFilename := flag.String("logfile", "", "the log file to use (defaults to '' meaning no log file)")
	flag.Parse()

	// Configure logging
	if *logFilename != "" {
		logFile, err := os.OpenFile(*logFilename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err == nil {
			mw := io.MultiWriter(os.Stdout, logFile)
			log.SetOutput(mw)
		} else {
			log.Errorf("Failed to open logging file; using default stderr: %s", err)
		}
	}
	log.SetFormatter(&log.JSONFormatter{})
	log.SetLevel(log.DebugLevel)
	logLevel, err := log.ParseLevel(*logLevelStr)
	if err == nil {
		log.SetLevel(logLevel)
	} else {
		log.Errorf("Invalid loglevel '%s'. Using default 'debug'.", logLevel)
	}

	log.Info("Chatwoot service starting...")

	// Load configuration
	log.Infof("Reading config from %s...", *configPath)
	configJson, err := os.ReadFile(*configPath)
	if err != nil {
		log.Fatalf("Could not read config from %s: %s", *configPath, err)
	}

	// Default configuration values
	configuration = Configuration{
		AllowMessagesFromUsersOnOtherHomeservers: false,
		ChatwootBaseUrl:                          "https://app.chatwoot.com/",
		ListenPort:                               8080,
	}

	err = json.Unmarshal(configJson, &configuration)
	username := mid.UserID(configuration.Username)
	_, botHomeserver, err = username.Parse()
	if err != nil {
		log.Fatal("Couldn't parse username")
	}

	// Open the chatwoot database
	dbUri, err := url.Parse(configuration.DBConnectionString)
	if err != nil {
		log.Fatalf("Invalid database URI. %v", err)
	}

	dbType := ""
	dbDialect := ""
	switch dbUri.Scheme {
	case "postgres", "postgresql":
		dbType = "pgx"
		dbDialect = "postgres"
		break
	default:
		log.Fatalf("Invalid database scheme '%s'", dbUri.Scheme)
	}

	db, err := sql.Open(dbType, dbUri.String())
	if err != nil {
		log.Fatalf("Could not open chatwoot database. %v", err)
	}

	// Make sure to exit cleanly
	c := make(chan os.Signal, 1)
	signal.Notify(c,
		os.Interrupt,
		os.Kill,
		syscall.SIGABRT,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGQUIT,
		syscall.SIGTERM,
	)
	go func() {
		for range c { // when the process is killed
			log.Info("Cleaning up")
			db.Close()
			os.Exit(0)
		}
	}()

	// Initialize the send lock map
	userSendlocks = map[mid.UserID]*sync.Mutex{}

	stateStore = store.NewStateStore(db, dbDialect, username)
	if err := stateStore.CreateTables(); err != nil {
		log.Fatal("Failed to create the tables for chatwoot.", err)
	}

	// login to homeserver
	if access_token, err := stateStore.GetAccessToken(); err == nil && access_token != "" {
		log.Infof("Got access token: %s", access_token)
		client, err = mautrix.NewClient(configuration.Homeserver, username, access_token)
		if err != nil {
			log.Fatalf("Couldn't login to the homeserver.")
		}
	} else {
		log.Info("Using username/password auth")
		// Use password authentication if we didn't have an access
		// token yet.
		password, err := configuration.GetPassword()
		if err != nil {
			log.Fatalf("Could not read password from %s", configuration.PasswordFile)
		}
		client, err = mautrix.NewClient(configuration.Homeserver, "", "")
		if err != nil {
			panic(err)
		}
		_, err = DoRetry("login", func() (interface{}, error) {
			return client.Login(&mautrix.ReqLogin{
				Type: mautrix.AuthTypePassword,
				Identifier: mautrix.UserIdentifier{
					Type: mautrix.IdentifierTypeUser,
					User: username.String(),
				},
				Password:                 password,
				InitialDeviceDisplayName: "chatwoot",
				StoreCredentials:         true,
			})
		})
		if err != nil {
			log.Fatalf("Couldn't login to the homeserver.")
		}

		if err := stateStore.SetAccessToken(client.AccessToken); err != nil {
			log.Fatalf("Couldn't set access token %+v", err)
		}
	}

	// set the client store on the client.
	client.Store = stateStore

	accessToken, err := configuration.GetChatwootAccessToken()
	if err != nil {
		log.Fatalf("Could not read access token from %s", configuration.ChatwootAccessTokenFile)
	}
	chatwootApi = chatwootapi.CreateChatwootAPI(
		configuration.ChatwootBaseUrl,
		configuration.ChatwootAccountID,
		configuration.ChatwootInboxID,
		accessToken,
	)

	// Setup the crypto store
	sqlCryptoStore := mcrypto.NewSQLCryptoStore(
		db,
		dbDialect,
		username.String(),
		mid.DeviceID("Bot Host"),
		[]byte("chatwoot_cryptostore_key"),
		CryptoLogger{},
	)
	err = sqlCryptoStore.CreateTables()
	if err != nil {
		log.Error(err)
		log.Fatal("Could not create tables for the SQL crypto store.")
	}

	olmMachine = mcrypto.NewOlmMachine(client, &CryptoLogger{}, sqlCryptoStore, stateStore)
	err = olmMachine.Load()
	if err != nil {
		log.Errorf("Could not initialize encryption support. Encrypted rooms will not work.")
	}

	syncer := client.Syncer.(*mautrix.DefaultSyncer)
	// Hook up the OlmMachine into the Matrix client so it receives e2ee
	// keys and other such things.
	syncer.OnSync(func(resp *mautrix.RespSync, since string) bool {
		olmMachine.ProcessSyncResponse(resp, since)
		return true
	})

	syncer.OnEventType(mevent.StateMember, func(_ mautrix.EventSource, event *mevent.Event) {
		olmMachine.HandleMemberEvent(event)
		stateStore.SetMembership(event)

		if event.GetStateKey() == username.String() && event.Content.AsMember().Membership == mevent.MembershipInvite {
			log.Info("Joining ", event.RoomID)
			_, err := DoRetry("join room", func() (interface{}, error) {
				return client.JoinRoomByID(event.RoomID)
			})
			if err != nil {
				log.Errorf("Could not join channel %s. Error %+v", event.RoomID.String(), err)
			} else {
				log.Infof("Joined %s sucessfully", event.RoomID.String())
			}
		} else if event.GetStateKey() == username.String() && event.Content.AsMember().Membership.IsLeaveOrBan() {
			log.Infof("Left or banned from %s", event.RoomID)
		}
	})

	syncer.OnEventType(mevent.StateEncryption, func(_ mautrix.EventSource, event *mevent.Event) { stateStore.SetEncryptionEvent(event) })
	syncer.OnEventType(mevent.EventMessage, func(source mautrix.EventSource, event *mevent.Event) {
		stateStore.UpdateMostRecentEventIdForRoom(event.RoomID, event.ID)
		if VerifyFromAuthorizedUser(event.Sender) {
			go HandleMessage(source, event)
		}
	})
	syncer.OnEventType(mevent.EventRedaction, func(source mautrix.EventSource, event *mevent.Event) {
		stateStore.UpdateMostRecentEventIdForRoom(event.RoomID, event.ID)
		if VerifyFromAuthorizedUser(event.Sender) {
			go HandleRedaction(source, event)
		}
	})
	syncer.OnEventType(mevent.EventEncrypted, func(source mautrix.EventSource, event *mevent.Event) {
		stateStore.UpdateMostRecentEventIdForRoom(event.RoomID, event.ID)
		if !VerifyFromAuthorizedUser(event.Sender) {
			return
		}

		decryptedEvent, err := olmMachine.DecryptMegolmEvent(event)
		if err != nil {
			log.Warnf("Failed to decrypt message from %s in %s: %+v", event.Sender, event.RoomID, err)
		} else {
			log.Debugf("Received encrypted event from %s in %s", event.Sender, event.RoomID)
			if decryptedEvent.Type == mevent.EventMessage {
				go HandleMessage(source, decryptedEvent)
			} else if decryptedEvent.Type == mevent.EventRedaction {
				go HandleRedaction(source, decryptedEvent)
			}
		}
	})

	// Start the sync loop
	go func() {
		for {
			log.Debugf("Running sync...")
			err = client.Sync()
			if err != nil {
				log.Errorf("Sync failed. %+v", err)
			}
		}
	}()

	// Listen to the webhook
	http.HandleFunc("/", HandleWebhook)
	http.HandleFunc("/webhook", HandleWebhook)
	log.Infof("Webhook listening on port %d", configuration.ListenPort)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", configuration.ListenPort), nil))
}

func VerifyFromAuthorizedUser(sender mid.UserID) bool {
	if configuration.AllowMessagesFromUsersOnOtherHomeservers {
		return true
	}
	_, homeserver, err := sender.Parse()
	if err != nil {
		return false
	}

	return botHomeserver == homeserver
}
