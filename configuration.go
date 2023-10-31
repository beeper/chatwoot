package main

import (
	"os"
	"strings"

	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"
	"go.mau.fi/zeroconfig"
	"maunium.net/go/mautrix/id"
)

type BackfillConfiguration struct {
	ChatwootConversations     bool `yaml:"chatwoot_conversations"`
	ConversationIDStateEvents bool `yaml:"conversation_id_state_events"`
}

type HomeserverWhitelist struct {
	Enable  bool     `yaml:"enable"`
	Allowed []string `yaml:"allowed"`
}

type StartNewChat struct {
	Enable   bool   `yaml:"enable"`
	Endpoint string `yaml:"endpoint"`
	Token    string `yaml:"token"`
}

type Configuration struct {
	// Authentication settings
	Homeserver   string    `yaml:"homeserver"`
	Username     id.UserID `yaml:"username"`
	PasswordFile string    `yaml:"password_file"`

	// Chatwoot Authentication
	ChatwootBaseUrl         string `yaml:"chatwoot_base_url"`
	ChatwootAccessTokenFile string `yaml:"chatwoot_access_token_file"`
	ChatwootAccountID       int    `yaml:"chatwoot_account_id"`
	ChatwootInboxID         int    `yaml:"chatwoot_inbox_id"`

	// Database settings
	Database dbutil.Config `yaml:"database"`

	// Bot settings
	HomeserverWhitelist     HomeserverWhitelist `yaml:"homeserver_whitelist"`
	StartNewChat            StartNewChat        `yaml:"start_new_chat"`
	CanonicalDMPrefix       string              `yaml:"canonical_dm_prefix"`
	BridgeIfMembersLessThan int                 `yaml:"bridge_if_members_less_than"`
	RenderMarkdown          bool                `yaml:"render_markdown"`

	// Webhook listener settings
	ListenPort int `yaml:"listen_port"`

	// Logging configuration
	Logging zeroconfig.Config `yaml:"logging"`

	// Backfill configuration
	Backfill BackfillConfiguration `yaml:"backfill"`
}

func (c *Configuration) GetPassword(log *zerolog.Logger) (string, error) {
	log.Debug().Str("password_file", c.PasswordFile).Msg("reading password from file")
	buf, err := os.ReadFile(c.PasswordFile)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(buf)), nil
}

func (c *Configuration) GetChatwootAccessToken(log *zerolog.Logger) (string, error) {
	log.Debug().Str("access_token_file", c.ChatwootAccessTokenFile).Msg("reading access token from file")
	buf, err := os.ReadFile(c.ChatwootAccessTokenFile)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(buf)), nil
}
