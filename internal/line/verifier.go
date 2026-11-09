package line

import (
	"context"
	"errors"
	"strings"

	"github.com/amangeldievkuu/miniloyal/internal/apierr"
	"github.com/amangeldievkuu/miniloyal/internal/tenant"
)

// ChannelVerifier proves control of LINE credentials before the tenant
// service stores them. LINE only issues a stateless token for a valid channel
// ID and secret, so minting one is the proof.
type ChannelVerifier struct {
	tokens    *TokenSource
	messaging *Messaging
}

// NewChannelVerifier returns a verifier over the given clients.
func NewChannelVerifier(tokens *TokenSource, messaging *Messaging) *ChannelVerifier {
	return &ChannelVerifier{tokens: tokens, messaging: messaging}
}

var _ tenant.ChannelVerifier = (*ChannelVerifier)(nil)

// VerifyMessagingChannel mints a token for the Official Account's Messaging
// API channel and reads its bot info.
func (c *ChannelVerifier) VerifyMessagingChannel(ctx context.Context, channelID, channelSecret string) (tenant.OAInfo, error) {
	token, _, err := c.tokens.Mint(ctx, channelID, channelSecret)
	if err != nil {
		return tenant.OAInfo{}, mapCredentialError(err)
	}
	info, err := c.messaging.BotInfoWithToken(ctx, token)
	if err != nil {
		return tenant.OAInfo{}, mapCredentialError(err)
	}
	basic := info.BasicID
	if basic != "" && !strings.HasPrefix(basic, "@") {
		basic = "@" + basic
	}
	return tenant.OAInfo{BotUserID: info.UserID, BasicID: basic, DisplayName: info.DisplayName}, nil
}

// VerifyMiniAppChannel mints a token for the LINE MINI App (LINE Login)
// channel, which also accepts the stateless token endpoint.
func (c *ChannelVerifier) VerifyMiniAppChannel(ctx context.Context, channelID, channelSecret string) error {
	if _, _, err := c.tokens.Mint(ctx, channelID, channelSecret); err != nil {
		return mapCredentialError(err)
	}
	return nil
}

func mapCredentialError(err error) error {
	switch {
	case errors.Is(err, ErrInvalidCredentials):
		return apierr.Validation(map[string]string{"channel_secret": "LINE rejected these credentials"})
	case errors.Is(err, ErrUnavailable):
		return apierr.Unavailable("LINE is not reachable; try again").WithCause(err)
	default:
		return apierr.Unavailable("LINE verification failed; try again").WithCause(err)
	}
}
