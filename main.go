package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/dghubble/go-twitter/twitter"
	"github.com/dghubble/oauth1"
	"github.com/gin-gonic/gin"
	"gopkg.in/olahol/melody.v1"
)

type Rank struct {
	URL          string              `json:"url"`
	Count        int                 `json:"count"`
	LongCount    int                 `json:"longCount"`
	Retweet      int                 `json:"retweet"`
	Twit         string              `json:"twit"`
	CreateAt     time.Time           `json:"create_at"`
	ProfileImage string              `json:"profile_image"`
	ImageURLs    []string            `json:"image_urls"`
	Videos       []twitter.VideoInfo `json:"videos"`
}
type ResponseType struct {
	Type string `json:"type"`
}
type URLResponse struct {
	ResponseType
	Rank Rank `json:"rank"`
}
type RankingResponse struct {
	ResponseType
	Ranking     []Rank `json:"ranking"`
	LongRanking []Rank `json:"longRanking"`
}
type HourlyRankingResponse struct {
	Ranking []Rank               `json:"ranking"`
	Trends  []twitter.TrendsList `json:"trends"`
}

var imagesCount map[string]int     // Count in 2H
var imagesLongCount map[string]int // Count in 24H

var imagesRank map[string]Rank
var mux sync.Mutex

func pushImage(image string) {
	_, ok := imagesCount[image]
	if ok {
		imagesCount[image]++
	} else {
		imagesCount[image] = 1
	}

	_, ok = imagesLongCount[image]
	if ok {
		imagesLongCount[image]++
	} else {
		imagesLongCount[image] = 1
	}
}
func checkTime(t time.Time, limit int) bool {
	n := time.Now()
	d := n.Sub(t)
	if int(d.Hours()) < limit {
		return true
	}
	return false
}
func refreshRanking() {
	var nextImagesCount map[string]int
	nextImagesCount = make(map[string]int)
	for k, v := range imagesCount {
		r := imagesRank[k]
		if checkTime(r.CreateAt, 1) && r.Retweet > 2 {
			nextImagesCount[k] = v
		}
	}
	imagesCount = nextImagesCount

	nextImagesCount = make(map[string]int)
	for k, v := range imagesLongCount {
		r := imagesRank[k]
		if checkTime(r.CreateAt, 24) && r.Retweet > 2 {
			nextImagesCount[k] = v
		}
	}
	imagesLongCount = nextImagesCount

	var nextImagesRank map[string]Rank
	nextImagesRank = make(map[string]Rank)
	for k, _ := range imagesCount {
		nextImagesRank[k] = imagesRank[k]
	}
	for k, _ := range imagesLongCount {
		nextImagesRank[k] = imagesRank[k]
	}
	imagesRank = nextImagesRank

	fmt.Printf("imagesRank count %d, imagesCount %d, imagesLongCount %d\n", len(imagesRank), len(imagesCount), len(imagesLongCount))
}

func makeRanking(countAssoc *map[string]int) []Rank {
	type kv struct {
		Key   string
		Value int
	}
	var ss []kv
	var ranking []Rank

	for k, v := range *countAssoc {
		ss = append(ss, kv{k, v})
	}
	sort.Slice(ss, func(i, j int) bool {
		return ss[i].Value > ss[j].Value
	})
	for index, kv := range ss {
		fmt.Printf("get imagesRank %s %v\n", kv.Key, imagesRank[kv.Key])
		ranking = append(ranking, imagesRank[kv.Key])
		if index > 100 {
			break
		}
	}
	sort.Slice(ranking, func(i, j int) bool {
		return ranking[i].Retweet > ranking[j].Retweet
	})
	return ranking
}
func makeRegularRanking() []Rank {
	mux.Lock()
	defer func() { mux.Unlock() }()
	return makeRanking(&imagesCount)
}
func makeLongRanking() []Rank {
	mux.Lock()
	defer func() { mux.Unlock() }()
	return makeRanking(&imagesLongCount)
}

func initTwitter(client *twitter.Client, m *melody.Melody, stopCh, doneCh chan struct{}) {

	defer func() { close(doneCh) }()

	// Convenience Demux demultiplexed stream messages
	demux := twitter.NewSwitchDemux()
	demux.Tweet = func(tweet *twitter.Tweet) {
		//fmt.Println(tweet.Text) // raw
		// todo: filter japanese
		// todo: extract url
		ext := tweet.ExtendedEntities
		if ext == nil {
			fmt.Println("Not extended entities")
			return
		}
		media := ext.Media //tweet.Entities.Media

		retweetedStatus := tweet.RetweetedStatus
		retweet := 0
		createAt, err := time.Parse("Mon Jan 2 15:04:05 -0700 2006", tweet.CreatedAt)
		if err != nil {
			fmt.Println("Error: time format %v", err)
		}
		if retweetedStatus != nil {
			retweet = retweetedStatus.RetweetCount
			createAt, _ = time.Parse("Mon Jan 2 15:04:05 -0700 2006", tweet.RetweetedStatus.CreatedAt)
			tweet = retweetedStatus
		}
		if checkTime(createAt, 24) {
			isJp := false
			for _, r := range tweet.Text {
				if unicode.In(r, unicode.Hiragana) || unicode.In(r, unicode.Katakana) {
					isJp = true
					break
				}
			}
			if len(media) > 0 && isJp {
				fmt.Println("Media: " + media[0].MediaURL)
				mux.Lock()
				pushImage(media[0].MediaURL)
				var images []string
				var videos []twitter.VideoInfo
				for _, v := range media {
					images = append(images, v.MediaURL)
					videos = append(videos, v.VideoInfo)
				}

				r := Rank{URL: media[0].MediaURL,
					Count:        imagesCount[media[0].MediaURL],
					LongCount:    imagesLongCount[media[0].MediaURL],
					Retweet:      retweet,
					Twit:         "https://twitter.com/" + tweet.User.ScreenName + "/status/" + tweet.IDStr,
					CreateAt:     createAt,
					ProfileImage: tweet.User.ProfileImageURL,
					ImageURLs:    images,
					Videos:       videos,
				}
				imagesRank[media[0].MediaURL] = r
				refreshRanking()
				mux.Unlock()

				ur := URLResponse{ResponseType: ResponseType{Type: "url"}, Rank: r}
				j, _ := json.Marshal(ur)
				m.Broadcast([]byte(j))
			} else {
				fmt.Println("Not include media")
			}
		}
		// todo: expand url

	}
	fmt.Println("Starting Stream...")

	// FILTER
	/*
		filterParams := &twitter.StreamFilterParams{
			Track:         []string{"http,。,、,,あ,い,う,え,お,か,き,く,け,こ,さ,し,す,せ,そ,た,ち,つ,て,と,な,に,ぬ,ね,の,は,ひ,ふ,へ,ほ,ま,み,む,め,も,や,ゆ,よ,ら,り,る,れ,ろ,わ,を,ん"}, // keyword
			//Track:         []string{"RT"},
			StallWarnings: twitter.Bool(true),
			Language:      []string{"ja"},
		}
		stream, err := client.Streams.Filter(filterParams)
		if err != nil {
			log.Fatal(err)
		}
	*/

	for {
		params := &twitter.StreamSampleParams{
			StallWarnings: twitter.Bool(true),
		}
		stream, err := client.Streams.Sample(params)
		if err != nil {
			log.Fatal(err)
		}

		// Receive messages until stopped or stream quits
		demux.HandleChan(stream.Messages)
		fmt.Println("retry")
		time.Sleep(10 * time.Second)

		select {
		case <-stopCh:
			stream.Stop()
			return
		default:
		}
	}
}

func main() {
	flags := flag.NewFlagSet("user-auth", flag.ExitOnError)
	consumerKey := flags.String("consumer-key", "", "Twitter Consumer Key")
	consumerSecret := flags.String("consumer-secret", "", "Twitter ConsumerSecret")
	accessToken := flags.String("access-token", "", "Twitter AccessToken")
	accessSecret := flags.String("access-secret", "", "Twitter AccessSecret")
	flags.Parse(os.Args[1:])

	if *consumerKey == "" || *consumerSecret == "" || *accessToken == "" || *accessSecret == "" {
		log.Fatal("Consumer key/secret and Access token/secret required")
	}
	imagesCount = make(map[string]int)
	imagesLongCount = make(map[string]int)
	imagesRank = make(map[string]Rank)

	r := gin.Default()
	m := melody.New()

	config := oauth1.NewConfig(*consumerKey, *consumerSecret)
	token := oauth1.NewToken(*accessToken, *accessSecret)
	// OAuth1 http.Client will automatically authorize Requests
	httpClient := config.Client(oauth1.NoContext, token)
	// Twitter Client
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	client := twitter.NewClient(httpClient)
	go initTwitter(client, m, stopCh, doneCh)

	r.GET("/trend", func(c *gin.Context) {
		trends, _, err := client.Trends.Place(23424856, nil)
		if err != nil {
			fmt.Println("error get trends %v", err)
		}
		c.JSON(http.StatusOK, trends)
	})
	r.GET("/ranking", func(c *gin.Context) {
		trends, _, err := client.Trends.Place(23424856, nil)
		if err != nil {
			fmt.Println("error get trends %v", err)
		}

		c.JSON(http.StatusOK, &HourlyRankingResponse{
			Ranking: makeRegularRanking(),
			Trends:  trends,
		})
	})
	r.GET("/longRanking", func(c *gin.Context) {
		trends, _, err := client.Trends.Place(23424856, nil)
		if err != nil {
			fmt.Println("error get trends %v", err)
		}

		c.JSON(http.StatusOK, &HourlyRankingResponse{
			Ranking: makeLongRanking(),
			Trends:  trends,
		})
	})
	r.GET("/ws", func(c *gin.Context) {
		m.HandleRequest(c.Writer, c.Request)
	})
	m.HandleMessage(func(s *melody.Session, msg []byte) {
		rr := RankingResponse{
			ResponseType: ResponseType{Type: "ranking"},
			Ranking:      makeRegularRanking(),
			LongRanking:  makeLongRanking(),
		}
		j, _ := json.Marshal(rr)
		s.Write([]byte(j))
	})
	go r.Run(":8088")

	// Wait for SIGINT and SIGTERM (HIT CTRL-C)
	ch := make(chan os.Signal)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	log.Println(<-ch)

	fmt.Println("Stopping Stream...")
	close(stopCh)
	<-doneCh
}
