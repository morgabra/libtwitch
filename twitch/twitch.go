package main

import (
	"context"
	"fmt"
	logger "log"
	"net/http"
	"os"
	"os/signal"

	"github.com/urfave/cli"

	"github.com/morgabra/libtwitch"
)

var log *logger.Logger = logger.New(os.Stdout, "", 0)

var clientID, clientSecret, webhookCallback string
var debug bool

func printUser(user *libtwitch.User) {
	log.Printf("name:%s id:%s views:%d type:%s\n", user.Login, user.ID, user.ViewCount, user.BroadcasterType)
}

func printGame(game *libtwitch.Game) {
	log.Printf("name:%s id:%s art:%s\n", game.Name, game.ID, game.BoxArtURL)
}

func printStream(stream *libtwitch.Stream) {
	log.Printf("user:%s game:%s title:%d viewers:%d\n", stream.UserID, stream.GameID, stream.Title, stream.ViewerCount)
}

func makeClient(ctx *cli.Context) *libtwitch.TwitchClient {

	if clientID == "" {
		log.Fatal("Missing required argument 'oauth-client-id'")
	}

	c, err := libtwitch.NewTwitchClient(context.Background(), clientID, clientSecret, webhookCallback, debug)
	if err != nil {
		log.Fatal(err)
	}

	return c
}

func main() {

	app := cli.NewApp()
	app.Name = "twitch"
	app.Version = "0.0.1"
	app.Usage = "Twitch API"
	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:        "oauth-client-id",
			Usage:       "Twitch OAuth application client id.",
			EnvVar:      "LIBTWITCH_CLIENT_ID",
			Destination: &clientID,
		},
		cli.StringFlag{
			Name:        "oauth-client-secret",
			Usage:       "Twitch OAuth application client secret. (optional)",
			EnvVar:      "LIBTWITCH_CLIENT_SECRET",
			Destination: &clientSecret,
		},
		cli.StringFlag{
			Name:        "callback-url",
			Usage:       "Webhook callback url. (optional)",
			EnvVar:      "LIBTWITCH_CALLBACK_URL",
			Destination: &webhookCallback,
		},
		cli.BoolFlag{
			Name:        "debug",
			Usage:       "Log requests and debugging info.",
			EnvVar:      "LIBTWITCH_DEBUG",
			Destination: &debug,
		},
	}

	app.Commands = []cli.Command{
		GetUser,
		GetGame,
		GetStream,
		WatchStream,
	}

	err := app.Run(os.Args)
	if err != nil {
		log.Fatal(err)
	}
}

var GetUser = cli.Command{
	Name:  "get-user",
	Usage: "Get user",
	Action: func(ctx *cli.Context) error {
		c := makeClient(ctx)

		if len(ctx.Args()) != 1 {
			log.Fatal("Missing required argument: username")
		}

		userName := ctx.Args()[0]

		user, err := c.GetUserByName(userName)
		if err != nil {
			log.Fatalf("Error: %s", err)
		}
		printUser(user)
		return nil
	},
}

var GetGame = cli.Command{
	Name:  "get-game",
	Usage: "Get game",
	Action: func(ctx *cli.Context) error {
		c := makeClient(ctx)

		if len(ctx.Args()) != 1 {
			log.Fatal("Missing required argument: game")
		}

		gameName := ctx.Args()[0]

		game, err := c.GetGameByName(gameName)
		if err != nil {
			log.Fatalf("Error: %s", err)
		}
		printGame(game)
		return nil
	},
}

var GetStream = cli.Command{
	Name:  "get-stream",
	Usage: "Get stream for user",
	Action: func(ctx *cli.Context) error {
		c := makeClient(ctx)

		if len(ctx.Args()) != 1 {
			log.Fatal("Missing required argument: username")
		}

		userName := ctx.Args()[0]

		stream, err := c.GetStreamByUserName(userName)
		if err != nil {
			log.Fatalf("Error: %s", err)
		}
		printStream(stream)
		return nil
	},
}

var WatchStream = cli.Command{
	Name:  "watch-stream",
	Usage: "Watch stream up/down events for user",
	Action: func(ctx *cli.Context) error {
		c := makeClient(ctx)

		if len(ctx.Args()) != 1 {
			log.Fatal("Missing required argument: username")
		}

		if webhookCallback == "" {
			log.Fatal("Missing required argument: --callback-url")
		}

		userName := ctx.Args()[0]

		user, err := c.GetUserByName(userName)
		if err != nil {
			log.Fatalf("Error: %s", err)
		}
		printUser(user)

		http.HandleFunc("/", c.WebhookHandler())

		go http.ListenAndServe(":9876", nil)

		sw, err := c.WatchStream(user.ID)
		if err != nil {
			return err
		}

		fw, err := c.WatchFollows(user.ID)
		if err != nil {
			return err
		}
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt)

		for {
			select {
			case follow := <-fw.Follows():

				fmt.Printf("Got follow event: %+v\n", follow)

				from, err := c.GetUserByID(follow.FromID)
				if err != nil {
					fmt.Printf("Error fetching user: %s\n", err.Error())
				}

				to, err := c.GetUserByID(follow.ToID)
				if err != nil {
					fmt.Printf("Error fetching user: %s\n", err.Error())
				}

				fmt.Printf("%s(%s) is following %s(%s)", from.DisplayName, from.ID, userName, to.ID)

			case stream := <-sw.Streams():

				fmt.Printf("Got stream event: %+v\n", stream)

				if stream == nil {
					fmt.Printf("%s stopped streaming.\n", userName)
					continue
				}

				game, err := c.GetGameByID(stream.GameID)
				if err != nil {
					fmt.Printf("Error fetching game: %s\n", err.Error())
				}

				fmt.Printf("%s is live. game: %s\n", userName, game.Name)

			case <-ch:
				c.Close()
				return nil
			}
		}
		return nil
	},
}
