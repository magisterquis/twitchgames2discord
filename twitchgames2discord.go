// Program twitchgames2discord posts a notification to a discord webhook when
// a twitch stream for a game goes live.
package main

/*
 * twitchgames2discord.go
 * Notify a discord webhook when a stream starts
 * By J. Stuart McMurray
 * Created 20200630
 * Last Modified 20200630
 */

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	lru "github.com/hashicorp/golang-lru"
)

const (
	// OAuthRenew is how long before the token expires to get a new one
	OAuthRenew = time.Minute
	/* resBuflen is the length of the response body to print when there's a
	non-200 response */
	resBuflen = 256
	/* cacheLen is the number of seen streams to cache */
	cacheLen = 10240
	/* nReqStream is the number of streams to request */
	nReqStream = 100
)

var (
	/* secret is the Twitch oauth secret */
	secret string
	// ErrorTooFast is returned when we get an HTTP 429
	ErrorTooFast = errors.New("HTTP requests are being made too frequently")
	/* cache is the LRU cache for keeping track of seen stream IDs */
	cache *lru.Cache
	/* warnedAboutMaxStreams is true if we've already warned the user that
	we've requested as many streams as we're going to */
	warnedAboutMaxStreams bool
	/* discordL is held while sending a stream to Discord */
	discordL sync.Mutex
)

// Stream describes a twitch stream
type Stream struct {
	ID       string
	User     string `json:"user_name"`
	Title    string
	Language string
}

func init() {
	var err error
	if cache, err = lru.New(cacheLen); nil != err {
		panic(err)
	}
}

func main() {
	var (
		gameName = flag.String(
			"game-name",
			"",
			"Twitch game `name`",
		)
		gameID = flag.String(
			"game-id",
			"",
			"Twitch game `ID`",
		)
		webhookURL = flag.String(
			"discord",
			"",
			"Discord webhook `URL`",
		)
		clientID = flag.String(
			"twitch-id",
			"",
			"Twitch client `ID`",
		)
		pollInterval = flag.Duration(
			"interval",
			30*time.Second,
			"Twitch poll `interval`",
		)
		secretFile = flag.String(
			"secret",
			".twitchgames2discord.secret",
			"Twitch secret `file`",
		)
	)
	flag.Usage = func() {
		fmt.Fprintf(
			os.Stderr,
			`Usage: %s [options]

Polls Twitch for streams for a specific game, specified either by game name
or game ID, and when a new stream is started reports it to a Discord webhook.

Options:
`,
			os.Args[0],
		)
		flag.PrintDefaults()
	}
	flag.Parse()

	log.SetOutput(os.Stdout)

	/* Get the OAuth secret */
	if "" == secret {
		/* Read the secret */
		buf, err := ioutil.ReadFile(*secretFile)
		if nil != err {
			log.Fatalf(
				"Error reading Twitch API secret from %s: %s",
				*secretFile,
				err,
			)
		}
		secret = strings.TrimSpace(string(buf))
		if "" == secret {
			log.Fatalf(
				"No Twitch API secret read from %s",
				*secretFile,
			)
		}
		log.Printf("Read Twitch API secret from %s", *secretFile)
	}

	/* Get the initial OAuth token */
	if "" == *clientID {
		log.Fatalf("Please specify a Twitch Client ID (-twitch-id)")
	}
	oauth, oauthExpiry, err := getOAuth(*clientID)
	if nil != err {
		log.Fatalf("Error getting OAuth token: %s", err)
	}
	oauthExpiry.Add(-OAuthRenew)
	log.Printf("Got initial Twitch OAuth token")

	/* Get the game ID if we don't have it already */
	if "" == *gameID && "" == *gameName {
		log.Fatalf(
			"Need either a game ID (-game-id) or " +
				"a game name (-game-name)",
		)
	}
	if "" == *gameID {
		var err error
		*gameName, *gameID, err = getGameID(
			*clientID,
			oauth,
			*gameName,
		)
		if nil != err {
			log.Fatalf(
				"Error getting game ID for %q: %s",
				*gameName,
				err,
			)
		}
		log.Printf("Game ID: %s", *gameID)
	}

	/* Poll the server every so often */
	for {
		/* Get an OAuth token if we need one */
		if time.Now().After(oauthExpiry) {
			oauth, oauthExpiry, err = getOAuth(*clientID)
			if nil != err {
				log.Printf(
					"Error getting OAuth token: %s",
					err,
				)
				time.Sleep(*pollInterval)
				continue
			}
			oauthExpiry.Add(-OAuthRenew)
			log.Printf("Got new OAuth token")
		}

		/* Get the list of streams */
		streamsList, err := getStreams(*clientID, oauth, *gameID)
		if errors.Is(err, ErrorTooFast) {
			log.Printf(
				"Rate-limiting in effect: increase the " +
					"poll interval (-interval)",
			)
			time.Sleep(*pollInterval)
			continue
		}
		if nil != err {
			log.Printf("Error getting games list: %s", err)
			time.Sleep(*pollInterval)
			continue
		}
		/* Send the new ones off to Discord */
		go sendNewToDiscord(
			*webhookURL,
			*gameName,
			streamsList,
		)

		/* Tell the user if there's too many streams */
		if nReqStream <= len(streamsList) {
			if !warnedAboutMaxStreams {
				warnedAboutMaxStreams = true
				log.Printf(
					"Got %d streams, but there may be "+
						"more - bug the dev about this",
					len(streamsList),
				)
			}
		}
		time.Sleep(*pollInterval)
	}
}

/* getOAuth gets the OAuth bearer token from the Twitch dev ID and the secret
in the named file.  It returns the token and the time the token expires. */
func getOAuth(clientID string) (string, time.Time, error) {
	/* Send the request for the token */
	var s struct {
		Exp json.Number `json:"expires_in"`
		Tok string      `json:"access_token"`
	}
	if err := request(
		"https://id.twitch.tv/oauth2/token",
		clientID,
		"",
		http.MethodPost,
		url.Values{
			"client_id":     []string{clientID},
			"client_secret": []string{secret},
			"grant_type":    []string{"client_credentials"},
		},
		&s,
	); nil != err {
		return "", time.Time{}, fmt.Errorf(
			"requesting OAuth token: %w",
			err,
		)
	}

	/* Work out the expiry time */
	exp, err := s.Exp.Int64()
	if nil != err {
		return "", time.Time{}, fmt.Errorf(
			"decoding expiry time: %w",
			err,
		)
	}

	return s.Tok, time.Now().Add(time.Second * time.Duration(exp)), nil
}

/* getGameID gets the twitch Game ID from a game name.  If multiple IDs match
it will print a table and exit the program. */
func getGameID(id, oauth, rname string) (name, gameID string, err error) {
	var s struct {
		Data []struct {
			ID   string
			Name string
		}
	}
	/* Get possible game IDs */
	if err := request(
		"https://api.twitch.tv/helix/games",
		id,
		oauth,
		http.MethodGet,
		url.Values{"name": []string{rname}},
		&s,
	); nil != err {
		return "", "", fmt.Errorf(
			"requesting IDs from server: %s",
			err,
		)
	}

	/* If we got none, typo */
	switch len(s.Data) {
	case 0: /* Didn't find one */
		return "", "", fmt.Errorf("no ID found")
	case 1: /* We win */
		return s.Data[0].Name, s.Data[0].ID, nil
	}

	/* We got more than one so print a table and ask the user to choose */
	tw := tabwriter.NewWriter(os.Stdout, 2, 8, 2, ' ', 0)
	fmt.Printf("Found multiple possible Game IDs for %s.\n", rname)
	fmt.Fprintf(tw, "ID\tName\n--\t----\n")
	for _, d := range s.Data {
		fmt.Fprintf(tw, "%s\t%s\n", d.ID, d.Name)
	}
	tw.Flush()
	os.Exit(1)

	panic("unpossible")
}

/* request sends an HTTP request to the twitch API at URL u and tries to
unmarshal the body of the response into body */
func request(
	u string,
	id string,
	oauth string,
	method string,
	form url.Values,
	body interface{},
) error {
	/* Work out the URL and body */
	f := form.Encode()
	var rbody io.Reader
	switch method {
	case http.MethodGet:
		u += "?" + f
	case http.MethodPost:
		rbody = strings.NewReader(f)
	default:
		return fmt.Errorf("unsupported method %s", method)
	}

	/* Request to send */
	req, err := http.NewRequest(method, u, rbody)
	if nil != err {
		return fmt.Errorf("making request: %w", err)
	}

	/* Set the request headers */
	if "" != id {
		req.Header.Set("Client-ID", id)
	}
	if "" != oauth {
		req.Header.Set("Authorization", "Bearer "+oauth)
	}
	req.Header.Set("Content-type", "application/x-www-form-urlencoded")

	res, err := http.DefaultClient.Do(req)
	if nil != err {
		return fmt.Errorf("POST request: %w", err)
	}
	defer res.Body.Close()

	/* Handle non-200 responses */
	if http.StatusTooManyRequests == res.StatusCode {
		return ErrorTooFast
	}
	if http.StatusOK != res.StatusCode {
		/* Get a bit of the response body to return */
		b := make([]byte, resBuflen)
		n, err := res.Body.Read(b)
		if nil != err && !errors.Is(err, io.EOF) {
			return fmt.Errorf(
				"reading non-OK response body: %w",
				err,
			)
		}
		switch n {
		case 0: /* No response body */
			return fmt.Errorf("non-OK response %s", res.Status)
		default: /* Got something to tell the user */
			return fmt.Errorf(
				"non-OK response %s: %q",
				res.Status,
				b[:n],
			)
		}
	}

	/* Unmarshal the body */
	if err := json.NewDecoder(res.Body).Decode(body); nil != err {
		return fmt.Errorf("unmarshalling response: %w", err)
	}
	return nil
}

/* getStreams gets a list of streams for the game  */
func getStreams(id, oauth, gameid string) ([]Stream, error) {
	/* Get a list of streams */
	var s struct {
		Data []Stream
	}
	if err := request(
		"https://api.twitch.tv/helix/streams",
		id,
		oauth,
		http.MethodGet,
		url.Values{
			"game_id": []string{gameid},
			"first":   []string{strconv.Itoa(nReqStream)},
		},
		&s,
	); nil != err {
		return nil, fmt.Errorf("requesting streams: %w", err)
	}

	return s.Data, nil
}

/* sendNewToDiscord sends new streams to discord via the webhook URL */
func sendNewToDiscord(wurl, gameName string, streams []Stream) {
	/* Send off the new streams */
	for _, stream := range streams {
		/* If we've already seen this one, ignore it */
		if ok, _ := cache.ContainsOrAdd(stream.ID, nil); ok {
			continue
		}
		log.Printf(
			"New Stream: [Game:%s] [ID:%s] [User:%q] [Title:%q]",
			gameName,
			stream.ID,
			stream.User,
			stream.Title,
		)

		/* If we've not seen it, send it off */
		go sendToDiscord(wurl, gameName, stream)
	}
}

/* sendToDiscord sends the stream to discord using the webhook URL.
discordL is held while sending. */
func sendToDiscord(wurl, gameName string, stream Stream) {
	/* Message to send to Discord */
	msg := fmt.Sprintf(
		"```"+`
Game:      %s
Streamer:  %s
Title:     %q
Language:  %s`+"```https://twitch.tv/%s",
		gameName,
		stream.User,
		stream.Title,
		stream.Language,
		stream.User,
	)

	/* Wait our turn */
	discordL.Lock()
	defer discordL.Unlock()

	/* Sleep time, as mandated by an HTTP 429 */
	var st time.Duration

	for {
		time.Sleep(st)

		/* Try to it off */
		res, err := http.Post(
			wurl,
			"application/x-www-form-urlencoded",
			strings.NewReader(url.Values{
				"content": []string{msg},
			}.Encode()),
		)
		if nil != err {
			log.Printf(
				"Error sending stream %s to Discord: %s",
				stream.ID,
				err,
			)
			return
		}
		defer res.Body.Close()

		/* If all went well, we're done */
		switch res.StatusCode {
		case http.StatusOK, /* What we expect */
			http.StatusNoContent:
			return
		case http.StatusTooManyRequests: /* Try again */
			/* Figure out how long to wait */
			var s struct {
				Wait json.Number `json:"retry_after"`
			}
			if err := json.NewDecoder(res.Body).Decode(
				&s,
			); nil != err {
				log.Fatalf(
					"Error parsing Discord "+
						"rate-limiting message: %s",
					err,
				)
			}
			/* Maybe a number? */
			n, err := s.Wait.Int64()
			if nil != err {
				log.Fatalf(
					"Error parsing Discord rate-limit "+
						"wait time %q: %s",
					s.Wait.String(),
					err,
				)
			}
			/* Wait that long and try again */
			st = time.Duration(n) * time.Millisecond
			continue
		}

		/* If we got anything else, log it */
		log.Printf("Unexpected Discord response: %s", res.Status)
		return
	}
}
