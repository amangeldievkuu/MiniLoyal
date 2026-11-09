package tenant

import "context"

// OAInfo is what LINE reports about a Messaging API channel once its
// credentials prove valid.
type OAInfo struct {
	BotUserID   string
	BasicID     string
	DisplayName string
}

// ChannelVerifier proves control of LINE credentials before they are stored,
// so one tenant cannot register another brand's public channel IDs. The
// line package implements it by minting a stateless channel access token
// with the submitted ID and secret.
type ChannelVerifier interface {
	// VerifyMessagingChannel validates an Official Account's Messaging API
	// channel and returns its bot info.
	VerifyMessagingChannel(ctx context.Context, channelID, channelSecret string) (OAInfo, error)
	// VerifyMiniAppChannel validates a LINE MINI App (LINE Login) channel.
	VerifyMiniAppChannel(ctx context.Context, channelID, channelSecret string) error
}

// SetChannelVerifier installs the verifier. Without one, credentials are
// stored unverified (development and tests).
func (s *Service) SetChannelVerifier(v ChannelVerifier) { s.verifier = v }
