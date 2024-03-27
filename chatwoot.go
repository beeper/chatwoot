package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v4/stdlib"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/hlog"
	globallog "github.com/rs/zerolog/log"
	"go.mau.fi/util/dbutil"
	"gopkg.in/yaml.v2"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/beeper/chatwoot/chatwootapi"
	"github.com/beeper/chatwoot/database"
)

var client *mautrix.Client
var configuration Configuration
var stateStore *database.Database

var chatwootAPI *chatwootapi.ChatwootAPI

var roomSendlocks map[id.RoomID]*sync.Mutex

var chatwootConversationIDType = event.Type{
	Type:  "com.beeper.chatwoot.conversation_id",
	Class: event.StateEventType,
}

type ChatwootConversationIDEventContent struct {
	ConversationID chatwootapi.ConversationID `json:"conversation_id"`
}

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
		HomeserverWhitelist:     HomeserverWhitelist{Enable: false},
		StartNewChat:            StartNewChat{Enable: false},
		ChatwootBaseUrl:         "https://app.chatwoot.com/",
		ListenPort:              8080,
		BridgeIfMembersLessThan: -1,
		RenderMarkdown:          false,
		Backfill: BackfillConfiguration{
			ChatwootConversations: true,
		},
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

	getLogger := func(evt *event.Event) zerolog.Logger {
		return log.With().
			Stringer("event_type", &evt.Type).
			Stringer("sender", evt.Sender).
			Str("room_id", string(evt.RoomID)).
			Str("event_id", string(evt.ID)).
			Logger()
	}

	log.Info().Any("configuration", configuration).Msg("Config loaded")
	log.Info().Msg("Chatwoot service starting...")
	ctx := log.WithContext(context.TODO())

	// Open the chatwoot database
	db, err := dbutil.NewFromConfig("chatwoot", configuration.Database, dbutil.ZeroLogger(*log))
	if err != nil {
		log.Fatal().Err(err).Msg("couldn't open database")
	}

	// Initialize the send lock map
	roomSendlocks = map[id.RoomID]*sync.Mutex{}

	stateStore = database.NewDatabase(db)
	if err := stateStore.DB.Upgrade(ctx); err != nil {
		log.Fatal().Err(err).Msg("failed to upgrade the Chatwoot database")
	}

	client, err = mautrix.NewClient(configuration.Homeserver, "", "")
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create matrix client")
	}
	client.Log = *log

	accessToken, err := configuration.GetChatwootAccessToken(log)
	if err != nil {
		log.Fatal().Err(err).Str("access_token_file", configuration.ChatwootAccessTokenFile).Msg("Could not read access token")
	}
	chatwootAPI = chatwootapi.CreateChatwootAPI(
		configuration.ChatwootBaseUrl,
		configuration.ChatwootAccountID,
		configuration.ChatwootInboxID,
		accessToken,
	)

	cryptoHelper, err := cryptohelper.NewCryptoHelper(client, []byte("chatwoot_cryptostore_key"), db)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create crypto helper")
	}
	password, err := configuration.GetPassword(log)
	if err != nil {
		log.Fatal().Err(err).Str("password_file", configuration.PasswordFile).Msg("Could not read password from ")
	}
	cryptoHelper.LoginAs = &mautrix.ReqLogin{
		Type:       mautrix.AuthTypePassword,
		Identifier: mautrix.UserIdentifier{Type: mautrix.IdentifierTypeUser, User: configuration.Username.String()},
		Password:   password,
	}
	cryptoHelper.DBAccountID = configuration.Username.String()
	cryptoHelper.DecryptErrorCallback = func(evt *event.Event, decryptErr error) {
		log := getLogger(evt)
		ctx := log.WithContext(context.TODO())
		log.Error().Err(decryptErr).Msg("Failed to decrypt message")

		stateStore.UpdateMostRecentEventIDForRoom(ctx, evt.RoomID, evt.ID)
		if !VerifyFromAuthorizedUser(ctx, evt.Sender) {
			return
		}

		conversationID, err := stateStore.GetChatwootConversationIDFromMatrixRoom(ctx, evt.RoomID)
		if err != nil {
			log.Warn().Err(err).Msg("no Chatwoot conversation associated with this room")
			return
		}

		DoRetry(ctx, fmt.Sprintf("send private error message to %d for %+v", conversationID, decryptErr), func(ctx context.Context) (*chatwootapi.Message, error) {
			return chatwootAPI.SendPrivateMessage(
				ctx,
				conversationID,
				fmt.Sprintf("**Failed to decrypt Matrix event (%s). You probably missed a message!**\n\nError: %+v", evt.ID, decryptErr))
		})
	}

	err = cryptoHelper.Init(ctx)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize crypto helper")
	}
	cryptoHelper.Machine().AllowKeyShare = AllowKeyShare
	client.Crypto = cryptoHelper

	addEvtContext := func(ctx context.Context, evt *event.Event) context.Context {
		return zerolog.Ctx(ctx).With().
			Stringer("event_type", &evt.Type).
			Stringer("sender", evt.Sender).
			Str("room_id", string(evt.RoomID)).
			Str("event_id", string(evt.ID)).
			Logger().
			WithContext(ctx)
	}
	syncer := client.Syncer.(*mautrix.DefaultSyncer)
	syncer.OnEventType(event.EventMessage, func(ctx context.Context, evt *event.Event) {
		ctx = addEvtContext(ctx, evt)
		stateStore.UpdateMostRecentEventIDForRoom(ctx, evt.RoomID, evt.ID)
		if VerifyFromAuthorizedUser(ctx, evt.Sender) {
			go HandleMessage(ctx, evt)
		}
	})
	syncer.OnEventType(event.EventReaction, func(ctx context.Context, evt *event.Event) {
		ctx = addEvtContext(ctx, evt)
		stateStore.UpdateMostRecentEventIDForRoom(ctx, evt.RoomID, evt.ID)
		if VerifyFromAuthorizedUser(ctx, evt.Sender) {
			go HandleReaction(ctx, evt)
		}
	})
	syncer.OnEventType(event.EventRedaction, func(ctx context.Context, evt *event.Event) {
		ctx = addEvtContext(ctx, evt)
		stateStore.UpdateMostRecentEventIDForRoom(ctx, evt.RoomID, evt.ID)
		if VerifyFromAuthorizedUser(ctx, evt.Sender) {
			go HandleRedaction(ctx, evt)
		}
	})

	syncCtx, cancelSync := context.WithCancel(context.Background())
	var syncStopWait sync.WaitGroup
	syncStopWait.Add(1)

	// Start the sync loop
	go func() {
		log.Debug().Msg("starting sync loop")
		err = client.SyncWithContext(syncCtx)
		defer syncStopWait.Done()
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Fatal().Err(err).Msg("Sync error")
		}
	}()

	// Make sure that there are conversations for all of the rooms that the bot
	// is in.
	// This is run every 24 hours.
	go func() {
		if !configuration.Backfill.ChatwootConversations && !configuration.Backfill.ConversationIDStateEvents {
			return
		}

		for {
			log := log.With().Str("component", "conversation_creation_backfill").Logger()
			ctx := log.WithContext(context.Background())

			log.Info().Msg("starting to create conversations for rooms that don't have a conversation yet")

			joined, err := client.JoinedRooms(ctx)
			if err != nil {
				log.Fatal().Err(err).Msg("Failed to get joined rooms")
			}

			for _, roomID := range joined.JoinedRooms {
				chatwootConversationID, err := stateStore.GetChatwootConversationIDFromMatrixRoom(ctx, roomID)
				if err != nil {
					// This room doesn't already has a Chatwoot conversation
					// associtaed with it.
					if configuration.Backfill.ChatwootConversations {
						err = backfillConversationForRoom(ctx, roomID)
						if err != nil {
							log.Warn().Err(err).Msg("Failed to backfill conversation for room")
							continue
						}
					}
				} else if configuration.Backfill.ConversationIDStateEvents {
					// If we already have a Chatwoot conversation, make sure that
					// the room has a state event with the Chatwoot conversation
					// ID.
					_, err = client.SendStateEvent(ctx, roomID, chatwootConversationIDType, "", ChatwootConversationIDEventContent{
						ConversationID: chatwootConversationID,
					})
					if err != nil {
						log.Warn().Err(err).Msg("Failed to send conversation_id state event")
					}
				}
			}

			log.Info().Msg("finished creating conversations for rooms that don't have a conversation yet... waiting 24 hours to backfill again")
			time.Sleep(24 * time.Hour)
		}
	}()

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
			cancelSync()
			db.RawDB.Close()
			os.Exit(0)
		}
	}()

	// Listen to the webhook
	handler := hlog.NewHandler(*log)(hlog.RequestIDHandler("request_id", "Request-ID")(http.HandlerFunc(HandleWebhook)))
	http.Handle("/", handler)
	http.Handle("/webhook", handler)
	log.Info().Int("listen_port", configuration.ListenPort).Msg("starting webhook listener")
	err = http.ListenAndServe(fmt.Sprintf(":%d", configuration.ListenPort), nil)
	if err != nil {
		log.Error().Err(err).Msg("creating the webhook listener failed")
	}

	cancelSync()
	syncStopWait.Wait()
	err = cryptoHelper.Close()
	if err != nil {
		log.Error().Err(err).Msg("Error closing database")
	}
}

func backfillConversationForRoom(ctx context.Context, roomID id.RoomID) error {
	log := zerolog.Ctx(ctx).With().Stringer("room_id", roomID).Logger()

	log.Info().Msg("Creating conversation for room")

	messages, err := client.Messages(ctx, roomID, "", "", mautrix.DirectionBackward, nil, 50)
	if err != nil {
		log.Err(err).Msg("Failed to get messages for room")
		return err
	}

	// Iterating through the messages will go in reverse order, so find
	// the most recent message event and use that to create the
	// conversation.
	for _, evt := range messages.Chunk {
		if evt.Type != event.EventMessage && evt.Type != event.EventEncrypted {
			continue
		}

		chatwootConversationID, err := GetOrCreateChatwootConversation(ctx, roomID, evt)
		if err != nil {
			log.Warn().Err(err).Msg("failed to get or create Chatwoot conversation")
			continue
		}

		log.Info().
			Int("chatwoot_conversation_id", int(chatwootConversationID)).
			Msg("created Chatwoot conversation")
		return nil
	}

	return fmt.Errorf("no messages found for room suitable for creating conversation")
}

func AllowKeyShare(ctx context.Context, device *id.Device, info event.RequestedKeyInfo) *crypto.KeyShareRejection {
	log := *zerolog.Ctx(ctx)

	// Always allow key requests from @help
	if device.UserID == configuration.Username {
		log.Info().Msg("allowing key share because it's another login of the help account")
		return nil
	}

	conversationID, err := stateStore.GetChatwootConversationIDFromMatrixRoom(ctx, info.RoomID)
	if err != nil {
		log.Info().Msg("no Chatwoot conversation found")
		return &crypto.KeyShareRejectNoResponse
	}
	log = log.With().Int("conversation_id", int(conversationID)).Logger()

	conversation, err := chatwootAPI.GetChatwootConversation(ctx, conversationID)
	if err != nil {
		log.Info().Err(err).Msg("couldn't get Chatwoot conversation")
		return &crypto.KeyShareRejectNoResponse
	}
	log = log.With().Int("sender_identifier", int(conversation.Meta.Sender.ID)).Logger()

	// This is the user that we expected for this Chatwoot conversation.
	if conversation.Meta.Sender.Identifier == device.UserID.String() {
		log.Info().Msg("Chatwoot conversation contact identifier matched device that was requesting the key. Allowing.")
		return nil
	} else {
		log.Info().Msg("rejecting key share request")
		return &crypto.KeyShareRejectNoResponse
	}
}

func VerifyFromAuthorizedUser(ctx context.Context, sender id.UserID) bool {
	log := zerolog.Ctx(ctx)
	if !configuration.HomeserverWhitelist.Enable {
		log.Debug().Msg("homeserver whitelist disabled, allowing all messages")
		return true
	}
	_, homeserver, err := sender.Parse()
	if err != nil {
		log.Warn().Err(err).Msg("failed to parse sender")
		return false
	}

	for _, allowedHS := range configuration.HomeserverWhitelist.Allowed {
		if homeserver == allowedHS {
			log.Debug().Str("sender_hs", allowedHS).Msg("allowing messages from whitelisted homeserver")
			return true
		}
	}
	log.Debug().Str("sender_hs", homeserver).Msg("rejecting messages from other homeserver")
	return false
}
