package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"

	_ "github.com/jackc/pgx/v4/stdlib"
	"github.com/rs/zerolog"
	globallog "github.com/rs/zerolog/log"
	"gopkg.in/yaml.v2"
	"maunium.net/go/mautrix"
	mcrypto "maunium.net/go/mautrix/crypto"
	mevent "maunium.net/go/mautrix/event"
	mid "maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/util/dbutil"

	"github.com/beeper/chatwoot/chatwootapi"
	"github.com/beeper/chatwoot/store"
)

var client *mautrix.Client
var configuration Configuration
var olmMachine *mcrypto.OlmMachine
var stateStore *store.StateStore

var chatwootApi *chatwootapi.ChatwootAPI
var botHomeserver string

var roomSendlocks map[mid.RoomID]*sync.Mutex

var VERSION = "0.2.1"

func main() {
	// Arg parsing
	configPath := flag.String("config", "./config.yaml", "config file location")
	flag.Parse()

	// Load configuration
	globallog.Info().Str("config_path", *configPath).Msg("Reading config")
	configYaml, err := os.ReadFile(*configPath)
	if err != nil {
		globallog.Fatal().Err(err).Str("config_path", *configPath).Msg("Failed reading the config")
	}

	// Default configuration values
	configuration = Configuration{
		AllowMessagesFromUsersOnOtherHomeservers: false,
		ChatwootBaseUrl:                          "https://app.chatwoot.com/",
		ListenPort:                               8080,
		BridgeIfMembersLessThan:                  -1,
		RenderMarkdown:                           false,
	}

	err = yaml.Unmarshal(configYaml, &configuration)
	if err != nil {
		globallog.Fatal().Err(err).Msg("Failed to parse configuration YAML")
	}

	// Setup logging
	log, err := configuration.Logging.Compile()
	if err != nil {
		globallog.Fatal().Err(err).Msg("Failed to compile logging configuration")
	}

	log.Info().Interface("configuration", configuration).Msg("Config loaded")
	username := mid.UserID(configuration.Username)
	_, botHomeserver, err = username.Parse()
	if err != nil {
		log.Fatal().Err(err).Msg("Couldn't parse username")
	}

	log.Info().Msg("Chatwoot service starting...")

	// Open the chatwoot database
	dbUri, err := url.Parse(configuration.DBConnectionString)
	if err != nil {
		log.Fatal().Err(err).Msg("Invalid database URI")
	}

	dbType := ""
	dbDialect := ""
	switch dbUri.Scheme {
	case "postgres", "postgresql":
		dbType = "pgx"
		dbDialect = "postgres"

	default:
		log.Fatal().Str("scheme", dbUri.Scheme).Msg("Invalid database scheme")
	}

	rawDB, err := sql.Open(dbType, dbUri.String())
	if err != nil {
		log.Fatal().Err(err).Msg("Could not open chatwoot database")
	}
	db, err := dbutil.NewWithDB(rawDB, dbDialect)
	if err != nil {
		log.Fatal().Err(err).Msg("Could not wrap chatwoot database.")
	}

	// Make sure to exit cleanly
	c := make(chan os.Signal, 1)
	signal.Notify(c,
		syscall.SIGABRT,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGQUIT,
		syscall.SIGTERM,
	)
	go func() {
		for range c { // when the process is killed
			log.Info().Msg("Cleaning up")
			db.RawDB.Close()
			os.Exit(0)
		}
	}()

	// Initialize the send lock map
	roomSendlocks = map[mid.RoomID]*sync.Mutex{}

	stateStore = store.NewStateStore(db, dbDialect, username)
	if err := stateStore.CreateTables(); err != nil {
		log.Fatal().Err(err).Msg("Failed to create the tables for chatwoot")
	}

	log.Info().Msg("Logging in")
	password, err := configuration.GetPassword(log)
	if err != nil {
		log.Fatal().Err(err).Str("password_file", configuration.PasswordFile).Msg("Could not read password from ")
	}
	deviceID := FindDeviceID(context.TODO(), db, username.String())
	if len(deviceID) > 0 {
		log.Info().Str("device_id", string(deviceID)).Msg("Found existing device ID in database")
	}
	client, err = mautrix.NewClient(configuration.Homeserver, "", "")
	if err != nil {
		panic(err)
	}
	_, err = DoRetry(context.TODO(), "login", func(context.Context) (*mautrix.RespLogin, error) {
		return client.Login(&mautrix.ReqLogin{
			Type: mautrix.AuthTypePassword,
			Identifier: mautrix.UserIdentifier{
				Type: mautrix.IdentifierTypeUser,
				User: username.String(),
			},
			Password:                 password,
			InitialDeviceDisplayName: "chatwoot",
			DeviceID:                 deviceID,
			StoreCredentials:         true,
		})
	})
	if err != nil {
		log.Fatal().Err(err).Msg("Couldn't login to the homeserver")
	}
	log.Info().Str("user_id", string(client.UserID)).Str("device_id", string(client.DeviceID)).Msg("Logged in")

	// set the client store on the client.
	client.Store = stateStore

	accessToken, err := configuration.GetChatwootAccessToken(log)
	if err != nil {
		log.Fatal().Err(err).Str("access_token_file", configuration.ChatwootAccessTokenFile).Msg("Could not read access token")
	}
	chatwootApi = chatwootapi.CreateChatwootAPI(
		configuration.ChatwootBaseUrl,
		configuration.ChatwootAccountID,
		configuration.ChatwootInboxID,
		accessToken,
	)

	// Setup the crypto store
	cryptoLogger := NewCryptoLogger(log)
	sqlCryptoStore := mcrypto.NewSQLCryptoStore(
		db,
		cryptoLogger,
		username.String(),
		client.DeviceID,
		[]byte("chatwoot_cryptostore_key"),
	)
	err = sqlCryptoStore.Upgrade()
	if err != nil {
		log.Fatal().Err(err).Msg("Could not create tables for the SQL crypto store")
	}

	olmMachine = mcrypto.NewOlmMachine(client, cryptoLogger, sqlCryptoStore, stateStore)
	olmMachine.AllowKeyShare = AllowKeyShare
	if err := olmMachine.Load(); err != nil {
		log.Fatal().Err(err).Msg("Could not initialize encryption support. Encrypted rooms will not work.")
	}

	syncer := client.Syncer.(*mautrix.DefaultSyncer)
	// Hook up the OlmMachine into the Matrix client so it receives e2ee
	// keys and other such things.
	syncer.OnSync(olmMachine.ProcessSyncResponse)

	getLogger := func(event *mevent.Event) zerolog.Logger {
		return log.With().
			Str("event_type", event.Type.String()).
			Str("sender", event.Sender.String()).
			Str("room_id", string(event.RoomID)).
			Str("event_id", string(event.ID)).
			Logger()
	}

	syncer.OnEventType(mevent.StateMember, func(_ mautrix.EventSource, event *mevent.Event) {
		log := getLogger(event)
		ctx := log.WithContext(context.TODO())

		olmMachine.HandleMemberEvent(event)
		stateStore.SetMembership(ctx, event)

		if event.GetStateKey() == username.String() && event.Content.AsMember().Membership == mevent.MembershipInvite {
			log.Info().Msg("Joining")
			_, err := DoRetry(ctx, "join room", func(context.Context) (*mautrix.RespJoinRoom, error) {
				return client.JoinRoomByID(event.RoomID)
			})
			if err != nil {
				log.Error().Err(err).Msg("Could not join channel")
			} else {
				log.Info().Msg("Joined sucessfully")
			}
		} else if event.GetStateKey() == username.String() && event.Content.AsMember().Membership.IsLeaveOrBan() {
			log.Info().Msg("Left or banned from room")
		}
	})

	syncer.OnEventType(mevent.StateEncryption, func(_ mautrix.EventSource, event *mevent.Event) {
		log := getLogger(event)
		ctx := log.WithContext(context.TODO())
		stateStore.SetEncryptionEvent(ctx, event)
	})
	syncer.OnEventType(mevent.EventMessage, func(source mautrix.EventSource, event *mevent.Event) {
		log := getLogger(event)
		ctx := log.WithContext(context.TODO())

		stateStore.UpdateMostRecentEventIdForRoom(ctx, event.RoomID, event.ID)
		if VerifyFromAuthorizedUser(event.Sender) {
			go HandleBeeperClientInfo(ctx, event)
			go HandleMessage(ctx, source, event)
		}
	})
	syncer.OnEventType(mevent.EventReaction, func(source mautrix.EventSource, event *mevent.Event) {
		log := getLogger(event)
		ctx := log.WithContext(context.TODO())

		stateStore.UpdateMostRecentEventIdForRoom(ctx, event.RoomID, event.ID)
		if VerifyFromAuthorizedUser(event.Sender) {
			go HandleBeeperClientInfo(ctx, event)
			go HandleReaction(ctx, source, event)
		}
	})
	syncer.OnEventType(mevent.EventRedaction, func(source mautrix.EventSource, event *mevent.Event) {
		log := getLogger(event)
		ctx := log.WithContext(context.TODO())

		stateStore.UpdateMostRecentEventIdForRoom(ctx, event.RoomID, event.ID)
		if VerifyFromAuthorizedUser(event.Sender) {
			go HandleBeeperClientInfo(ctx, event)
			go HandleRedaction(ctx, source, event)
		}
	})
	syncer.OnEventType(mevent.EventEncrypted, func(source mautrix.EventSource, event *mevent.Event) {
		log := getLogger(event)
		ctx := log.WithContext(context.TODO())

		stateStore.UpdateMostRecentEventIdForRoom(ctx, event.RoomID, event.ID)
		if !VerifyFromAuthorizedUser(event.Sender) {
			return
		}

		decryptedEvent, err := olmMachine.DecryptMegolmEvent(event)
		if err != nil {
			decryptErr := err
			log.Error().Err(err).Msg("Failed to decrypt message")
			conversationID, err := stateStore.GetChatwootConversationIDFromMatrixRoom(ctx, event.RoomID)

			if err != nil {
				log.Warn().Msg("no Chatwoot conversation associated with this room")
				return
			}

			DoRetry(ctx, fmt.Sprintf("send private error message to %d for %+v", conversationID, decryptErr), func(ctx context.Context) (*chatwootapi.Message, error) {
				return chatwootApi.SendPrivateMessage(
					ctx,
					conversationID,
					fmt.Sprintf("**Failed to decrypt Matrix event (%s). You probably missed a message!**\n\nError: %+v", event.ID, decryptErr))
			})
		} else {
			log.Debug().Msg("Received encrypted event")
			go HandleBeeperClientInfo(ctx, event)
			if decryptedEvent.Type == mevent.EventMessage {
				go HandleMessage(ctx, source, decryptedEvent)
			} else if decryptedEvent.Type == mevent.EventReaction {
				go HandleReaction(ctx, source, decryptedEvent)
			} else if decryptedEvent.Type == mevent.EventRedaction {
				go HandleRedaction(ctx, source, decryptedEvent)
			}
		}
	})

	// Start the sync loop
	go func() {
		log.Debug().Msg("starting sync loop")
		for {
			err = client.Sync()
			if err != nil {
				log.Error().Err(err).Msg("sync failed")
			}
		}
	}()

	// Listen to the webhook
	http.HandleFunc("/", HandleWebhook)
	http.HandleFunc("/webhook", HandleWebhook)
	log.Info().Int("listen_port", configuration.ListenPort).Msg("starting webhook listener")
	err = http.ListenAndServe(fmt.Sprintf(":%d", configuration.ListenPort), nil)
	if err != nil {
		log.Error().Err(err).Msg("creating the webhook listener wfailed")
	}
}

func AllowKeyShare(device *mid.Device, info mevent.RequestedKeyInfo) *mcrypto.KeyShareRejection {
	log := globallog.With().
		Str("device_id", device.UserID.String()).
		Str("room_id", info.RoomID.String()).
		Str("session_id", info.SessionID.String()).
		Logger()
	ctx := log.WithContext(context.TODO())

	// Always allow key requests from @help
	if device.UserID.String() == configuration.Username {
		log.Info().Msg("allowing key share because it's another login of the help account")
		return nil
	}

	conversationID, err := stateStore.GetChatwootConversationIDFromMatrixRoom(ctx, info.RoomID)
	if err != nil {
		log.Info().Msg("no Chatwoot conversation found")
		return &mcrypto.KeyShareRejectNoResponse
	}
	log = log.With().Int("conversation_id", conversationID).Logger()

	conversation, err := chatwootApi.GetChatwootConversation(conversationID)
	if err != nil {
		log.Info().Err(err).Msg("couldn't get Chatwoot conversation")
		return &mcrypto.KeyShareRejectNoResponse
	}
	log = log.With().Int("sender_identifier", conversation.Meta.Sender.ID).Logger()

	// This is the user that we expected for this Chatwoot conversation.
	if conversation.Meta.Sender.Identifier == device.UserID.String() {
		log.Info().Msg("Chatwoot conversation contact identifier matched device that was requesting the key. Allowing.")
		return nil
	} else {
		log.Info().Msg("rejecting key share request")
		return &mcrypto.KeyShareRejectNoResponse
	}
}

func FindDeviceID(ctx context.Context, db *dbutil.Database, accountID string) (deviceID mid.DeviceID) {
	err := db.QueryRowContext(ctx, "SELECT device_id FROM crypto_account WHERE account_id=$1", accountID).Scan(&deviceID)
	if err != nil && err != sql.ErrNoRows {
		globallog.Warn().Err(err).Msg("failed to scan device ID")
	}
	return
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
