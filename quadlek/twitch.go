package twitch

import (
	"context"
	"errors"
	"net/http"

	"strings"

	"fmt"

	log "github.com/sirupsen/logrus"
	"github.com/jirwin/quadlek/quadlek"
	"github.com/morgabra/libtwitch"
)

type TwitchFollow struct {
	SlackChannels []string // Slack channel(s) to send events

	TwitchUser   string // Twitch username
	SlackUser    string // Associated slack username (optional)
	WatchStream  bool   // Report twitch stream up/down events
	WatchFollows bool   // Report new twitch followers

	ctx context.Context

	user   *libtwitch.User
	stream *libtwitch.Stream

	streamWatcher libtwitch.Watcher
	followWatcher libtwitch.Watcher
}

var twitchClient *libtwitch.TwitchClient
var twitchClientCancel context.CancelFunc
var twitchWebhookHandler http.HandlerFunc
var twitchFollows []*TwitchFollow

func help(cmdMsg *quadlek.CommandMsg) {
	cmdMsg.Command.Reply() <- &quadlek.CommandResp{
		Text:      "twitch: report streamer activity.\nAvailable commands: help, live",
		InChannel: false,
	}
}

func sayError(cmdMsg *quadlek.CommandMsg, msg string, inChannel bool) {
	cmdMsg.Command.Reply() <- &quadlek.CommandResp{
		Text:      fmt.Sprintf("Uh Oh. Something broke: %s", msg),
		InChannel: inChannel,
	}
}

func say(cmdMsg *quadlek.CommandMsg, msg string, inChannel bool) {
	cmdMsg.Command.Reply() <- &quadlek.CommandResp{
		Text:      msg,
		InChannel: inChannel,
	}
}

func twitchWebhook(ctx context.Context, whChannel <-chan *quadlek.WebhookMsg) {
	for {
		select {
		case whMsg := <-whChannel:
			twitchWebhookHandler(whMsg.ResponseWriter, whMsg.Request)
			whMsg.Done <- true
		case <-ctx.Done():
			log.Info("twitch: stopping webhook handler")
			twitchClientCancel()
			return
		}
	}
}

func twitchCommand(ctx context.Context, cmdChannel <-chan *quadlek.CommandMsg) {
	for {
		select {
		case cmdMsg := <-cmdChannel:

			// /twitch <command> <args...>
			cmd := strings.SplitN(cmdMsg.Command.Text, " ", 1)
			if len(cmd) == 0 {
				help(cmdMsg)
				return
			}
			log.Infof("twitch: got command %s", cmd[0])
			switch cmd[0] {
			case "live":
				count := 0
				msg := ""
				for _, follow := range twitchFollows {
					if follow.stream != nil {
						count += 1
						gameName := "unknown"
						game, err := twitchClient.GetGameByID(follow.stream.GameID)
						if err == nil {
							gameName = game.Name
						}
						msg += fmt.Sprintf("\n%s is live (game: %s)", follow.user.DisplayName, gameName)
					}
				}
				cmdMsg.Command.Reply() <- &quadlek.CommandResp{
					Text:      fmt.Sprintf("%d followed users are live right now.%s", count, msg),
					InChannel: true,
				}
			default:
				help(cmdMsg)
			}

		case <-ctx.Done():
			log.Info("twitch: stopping command handler")
			twitchClientCancel()
			return
		}
	}
}

func watch(bot *quadlek.Bot, follow *TwitchFollow) {
	for {
		select {
		case <-follow.ctx.Done():
			return
		case stream := <-follow.streamWatcher.Streams():
			follow.stream = stream
			for _, scn := range follow.SlackChannels {
				scid, err := bot.GetChannelId(scn)
				if err != nil {
					log.WithError(err).Errorf("twitch: got stream event, but failed looking up slack channel id %s", scn)
					continue
				}

				if follow.stream != nil {
					bot.Say(scid, fmt.Sprintf("twitch: %s is live!", follow.user.DisplayName))
				}
			}
		case streamFollow := <-follow.followWatcher.Follows():
			for _, scn := range follow.SlackChannels {
				scid, err := bot.GetChannelId(scn)
				if err != nil {
					log.WithError(err).Errorf("twitch: got stream follow event, but failed looking up slack channel id %s", scn)
					continue
				}
				bot.Say(scid, fmt.Sprintf("twitch: %s has a new follower! (%s)", follow.user.DisplayName, streamFollow.FromID))
			}
		}
	}
}

func load(ctx context.Context, follows []*TwitchFollow) func(bot *quadlek.Bot, store *quadlek.Store) error {

	return func(bot *quadlek.Bot, store *quadlek.Store) error {

		for _, follow := range follows {

			follow.ctx = ctx

			// Look up twitch user
			user, err := twitchClient.GetUserByName(follow.TwitchUser)
			if err != nil {
				log.WithError(err).Errorf("twitch: failed fetching twitch user %s, skipping.", follow.TwitchUser)
				continue
			}
			log.Infof("twitch: %s exists.", follow.TwitchUser)
			follow.user = user

			// Look up if they happen to be streaming right now
			stream, err := twitchClient.GetStreamByUserID(user.ID)
			if err != nil {
				if err != libtwitch.ErrNotFound {
					log.WithError(err).Errorf("twitch: failed fetching twitch user %s stream, skipping.", follow.TwitchUser)
					continue
				}
			} else {
				log.Infof("twitch: %s is currently live.", follow.TwitchUser)
				follow.stream = stream
			}

			// (optionally) Start following stream events
			if follow.WatchStream {
				log.Infof("twitch: adding stream watcher for user %s", follow.TwitchUser)
				sw, err := twitchClient.WatchStream(user.ID)
				if err != nil {
					log.WithError(err).Errorf("twitch: failed to watch twitch user %s stream, skipping.", follow.TwitchUser)
					continue
				}
				follow.streamWatcher = sw
			} else {
				follow.streamWatcher = &libtwitch.NilStreamWatcher{}
			}

			// (optionally) Start following follow events
			if follow.WatchFollows {
				log.Infof("twitch: adding follow watcher for user %s", follow.TwitchUser)
				sw, err := twitchClient.WatchFollows(user.ID)
				if err != nil {
					log.WithError(err).Errorf("twitch: failed to watch twitch user %s follows, skipping.", follow.TwitchUser)
					continue
				}
				follow.followWatcher = sw
			} else {
				follow.followWatcher = &libtwitch.NilStreamWatcher{}
			}

			go watch(bot, follow)
		}

		twitchFollows = follows

		return nil
	}
}

func makeClient(ctx context.Context, oauthClientID, oauthSecret, webhookCallbackPath string, debug bool) (*libtwitch.TwitchClient, error) {

	if oauthClientID == "" {
		return nil, errors.New("OAuth ClientID is required.")
	}

	c, err := libtwitch.NewTwitchClient(ctx, oauthClientID, oauthSecret, webhookCallbackPath, debug)
	if err != nil {
		return nil, err
	}

	return c, nil
}

func Register(oauthClientID, oauthSecret, webhookCallbackPath string, debug bool, follows []*TwitchFollow) quadlek.Plugin {
	ctx, cancel := context.WithCancel(context.Background())
	client, err := makeClient(ctx, oauthClientID, oauthSecret, webhookCallbackPath, debug)
	if err != nil {
		log.WithError(err).Errorf("twitch: failed to create twitch client, bailing: %s", err)
		return nil
	}
	twitchClient = client
	twitchClientCancel = cancel
	twitchWebhookHandler = twitchClient.WebhookHandler()

	return quadlek.MakePlugin(
		"twitch",
		[]quadlek.Command{
			quadlek.MakeCommand("twitch", twitchCommand),
		},
		nil,
		nil,
		[]quadlek.Webhook{
			quadlek.MakeWebhook("twitch", twitchWebhook),
		},
		load(ctx, follows),
	)
}
