Twitch Games To Discord
=======================
Simple little program to poll Twitch for streams for a game and send them to
Discord via a webhook.

Games can be either specified by name or numeric ID.

Limitations:
- Only retrieves the top 100 streams for any game
- Twitch secret is only readable from a file or baked-in at compile time
- Discord URL is in the command line

Please run with `-h` for more options.

Setup
-----
You'll need the following bits of information
1.  A Discord [webhook](https://support.discord.com/hc/en-us/articles/228383668-Intro-to-Webhooks) URL
2.  A Twitch application (made on the [Twitch developer console](https://dev.twitch.tv/console/apps/create)) Client ID
3.  The same Twitch application's Client Secret

Download and build the program.  You'll need the [Go](https://golang.org/doc/install) compiler installed
```sh
go get github.com/magisterquis/twitchgames2discord
go install github.com/magisterquis/twitchgames2discord
```

Stick the secret in a file named `.twitchgames2discord.secret` in the same
directory from which you'll be running the program
```sh
echo 0123456789abcdefghijABCDEFGHIJ > .twitchgames2discord.secret
```

Run the program, substituting the Twitch Client ID, Discord Webhook, and game
name for your own.
```sh
twitchgames2discord \
        -twitch-id uo6dggojyb8d6soh92zknwmi5ej1q2 \
        -discord 'https://discordapp.com/api/webhooks/012345678901234567/o4YNqIdfYvmTBhlMlzhk5QFGb2uGHd2ToG61ftq6YF1IhFnTpVT-UQzJce_r3titKNR9' \
        -game-name 'executive assault 2'
```

Twitch Secret
-------------
Besides putting it in a file, the Twitch secret can be baked into the program
at compile time with `-ldflags '-X main.secret=0123456789abcdefghijABCDEFGHIJ'`
