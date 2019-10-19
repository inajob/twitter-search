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
	URLs         []string            `json:"urls"`
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
	Ranking        []Rank `json:"ranking"`
	LongRanking    []Rank `json:"longRanking"`
	UrlRanking     []Rank `json:"urlRanking"`
	UrlLongRanking []Rank `json:"urlLongRanking"`
}
type HourlyRankingResponse struct {
	Ranking    []Rank               `json:"ranking"`
	UrlRanking []Rank               `json:"urlRanking"`
	Trends     []twitter.TrendsList `json:"trends"`
}

var imagesCount map[string]int     // Count in 2H
var imagesLongCount map[string]int // Count in 24H

var urlsCount map[string]int     // Count in 2H
var urlsLongCount map[string]int // Count in 24H

var imagesRank map[string]Rank
var mux sync.Mutex

func pushTwit(twit string) {
	_, ok := imagesCount[twit]
	if ok {
		imagesCount[twit]++
	} else {
		imagesCount[twit] = 1
	}

	_, ok = imagesLongCount[twit]
	if ok {
		imagesLongCount[twit]++
	} else {
		imagesLongCount[twit] = 1
	}
}
func pushURL(twit string) {
	_, ok := urlsCount[twit]
	if ok {
		urlsCount[twit]++
	} else {
		urlsCount[twit] = 1
	}

	_, ok = urlsLongCount[twit]
	if ok {
		urlsLongCount[twit]++
	} else {
		urlsLongCount[twit] = 1
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
	// ============
	var nextImagesCount map[string]int
	nextImagesCount = make(map[string]int)
	for k, v := range imagesCount {
		r := imagesRank[k]
		if checkTime(r.CreateAt, 1) && r.Retweet > 5 {
			nextImagesCount[k] = v
		}
	}
	imagesCount = nextImagesCount

	nextImagesCount = make(map[string]int)
	for k, v := range imagesLongCount {
		r := imagesRank[k]
		if checkTime(r.CreateAt, 24) && r.Retweet > 5 {
			nextImagesCount[k] = v
		}
	}
	imagesLongCount = nextImagesCount

	// ============
	var nextUrlsCount map[string]int
	nextUrlsCount = make(map[string]int)
	for k, v := range urlsCount {
		r := imagesRank[k]
		if checkTime(r.CreateAt, 1) && r.Retweet > 5 {
			nextUrlsCount[k] = v
		}
	}
	urlsCount = nextUrlsCount

	nextUrlsCount = make(map[string]int)
	for k, v := range urlsLongCount {
		r := imagesRank[k]
		if checkTime(r.CreateAt, 24) && r.Retweet > 5 {
			nextUrlsCount[k] = v
		}
	}
	urlsLongCount = nextUrlsCount
	// ============

	var nextImagesRank map[string]Rank
	nextImagesRank = make(map[string]Rank)
	for k, _ := range imagesCount {
		nextImagesRank[k] = imagesRank[k]
	}
	for k, _ := range imagesLongCount {
		nextImagesRank[k] = imagesRank[k]
	}
	for k, _ := range urlsCount {
		nextImagesRank[k] = imagesRank[k]
	}
	for k, _ := range urlsLongCount {
		nextImagesRank[k] = imagesRank[k]
	}
	imagesRank = nextImagesRank

	fmt.Printf("imagesRank count:%d, imagesCount:%d, imagesLongCount:%d, urlsCount:%d, urlsLongCount:%d\n",
		len(imagesRank), len(imagesCount), len(imagesLongCount), len(urlsCount), len(urlsLongCount))
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
		//fmt.Printf("get imagesRank %s %v\n", kv.Key, imagesRank[kv.Key])
		ranking = append(ranking, imagesRank[kv.Key])
		if index > 200 {
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
func makeUrlRegularRanking() []Rank {
	mux.Lock()
	defer func() { mux.Unlock() }()
	return makeRanking(&urlsCount)
}
func makeUrlLongRanking() []Rank {
	mux.Lock()
	defer func() { mux.Unlock() }()
	return makeRanking(&urlsLongCount)
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
		var media []twitter.MediaEntity
		if ext != nil {
			media = ext.Media //tweet.Entities.Media
		}
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
			if isJp {
				//fmt.Println("Media: " + tweet.IDStr)
				isUseTwit := false
				if len(media) > 0 {
					isUseTwit = true
				}
				if len(tweet.Entities.Urls) > 0 {
					isUseTwit = true
				}
				if isUseTwit {
					mux.Lock()
					/*
					  TODO: url
					  url only => url
					  media only => first media url?
					*/
					pushTwit(tweet.IDStr)
					var images []string
					var videos []twitter.VideoInfo
					var urls []string
					for _, v := range media {
						images = append(images, v.MediaURL)
						videos = append(videos, v.VideoInfo)
					}

					if len(tweet.Entities.Urls) > 0 {
						for _, r := range tweet.Entities.Urls {
							urls = append(urls, r.ExpandedURL)
						}
					}
					mediaURL := ""
					if len(media) > 0 {
						mediaURL = media[0].MediaURL
					}
					r := Rank{URL: mediaURL,
						Count:        imagesCount[tweet.IDStr],
						LongCount:    imagesLongCount[tweet.IDStr],
						Retweet:      retweet,
						Twit:         "https://twitter.com/" + tweet.User.ScreenName + "/status/" + tweet.IDStr,
						CreateAt:     createAt,
						ProfileImage: tweet.User.ProfileImageURL,
						ImageURLs:    images,
						Videos:       videos,
						URLs:         urls,
					}
					imagesRank[tweet.IDStr] = r
					refreshRanking()
					mux.Unlock()

					ur := URLResponse{ResponseType: ResponseType{Type: "url"}, Rank: r}
					j, _ := json.Marshal(ur)
					m.Broadcast([]byte(j))
				}
			}

		}
		// todo: expand url
	}
	fmt.Println("Starting Stream...")

	// == sample stream ==
	for {
		params := &twitter.StreamSampleParams{
			StallWarnings: twitter.Bool(true),
		}
		streamSample, err := client.Streams.Sample(params)
		if err != nil {
			log.Fatal(err)
		}

		// Receive messages until stopped or stream quits
		demux.HandleChan(streamSample.Messages)
		fmt.Println("retry")
		time.Sleep(10 * time.Second)

		select {
		case <-stopCh:
			streamSample.Stop()
			return
		default:
		}
	}

}

func initTwitterFilter(client *twitter.Client, m *melody.Melody, stopCh, doneCh chan struct{}) {

	defer func() { close(doneCh) }()

	// Convenience Demux demultiplexed stream messages
	demux := twitter.NewSwitchDemux()
	demux.Tweet = func(tweet *twitter.Tweet) {
		//fmt.Println(tweet.Text) // raw
		// todo: filter japanese
		// todo: extract url
		ext := tweet.ExtendedEntities
		var media []twitter.MediaEntity
		if ext != nil {
			media = ext.Media //tweet.Entities.Media
		}
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
			if isJp {
				//fmt.Println("Media: " + tweet.IDStr)
				isUseTwit := false
				if len(media) > 0 {
					isUseTwit = true
				}
				if len(tweet.Entities.Urls) > 0 {
					isUseTwit = true
				}
				if isUseTwit {
					mux.Lock()
					/*
					  TODO: url
					  url only => url
					  media only => first media url?
					*/
					pushURL(tweet.IDStr)
					var images []string
					var videos []twitter.VideoInfo
					var urls []string
					for _, v := range media {
						images = append(images, v.MediaURL)
						videos = append(videos, v.VideoInfo)
					}

					if len(tweet.Entities.Urls) > 0 {
						for _, r := range tweet.Entities.Urls {
							urls = append(urls, r.ExpandedURL)
						}
					}
					mediaURL := ""
					if len(media) > 0 {
						mediaURL = media[0].MediaURL
					}
					r := Rank{URL: mediaURL,
						Count:        imagesCount[tweet.IDStr],
						LongCount:    imagesLongCount[tweet.IDStr],
						Retweet:      retweet,
						Twit:         "https://twitter.com/" + tweet.User.ScreenName + "/status/" + tweet.IDStr,
						CreateAt:     createAt,
						ProfileImage: tweet.User.ProfileImageURL,
						ImageURLs:    images,
						Videos:       videos,
						URLs:         urls,
					}
					imagesRank[tweet.IDStr] = r
					refreshRanking()
					mux.Unlock()

					ur := URLResponse{ResponseType: ResponseType{Type: "url"}, Rank: r}
					j, _ := json.Marshal(ur)
					m.Broadcast([]byte(j))
				}
			}

		}
		// todo: expand url
	}
	fmt.Println("Starting Stream...")

	// FILTER
	for {
		filterParams := &twitter.StreamFilterParams{
			Track:         []string{"http"}, // keyword
			StallWarnings: twitter.Bool(true),
			Language:      []string{"ja"},
		}
		streamFilter, err := client.Streams.Filter(filterParams)
		if err != nil {
			log.Fatal(err)
		}
		// Receive messages until stopped or stream quits
		demux.HandleChan(streamFilter.Messages)
		fmt.Println("retry")
		time.Sleep(10 * time.Second)

		select {
		case <-stopCh:
			streamFilter.Stop()
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

	urlsCount = make(map[string]int)
	urlsLongCount = make(map[string]int)

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
	//go initTwitterFilter(client, m, stopCh, doneCh)

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
			Ranking:    makeRegularRanking(),
			UrlRanking: makeUrlRegularRanking(),
			Trends:     trends,
		})
	})
	r.GET("/longRanking", func(c *gin.Context) {
		trends, _, err := client.Trends.Place(23424856, nil)
		if err != nil {
			fmt.Println("error get trends %v", err)
		}

		c.JSON(http.StatusOK, &HourlyRankingResponse{
			Ranking:    makeLongRanking(),
			UrlRanking: makeUrlLongRanking(),
			Trends:     trends,
		})
	})
	r.GET("/ws", func(c *gin.Context) {
		m.HandleRequest(c.Writer, c.Request)
	})
	m.HandleMessage(func(s *melody.Session, msg []byte) {
		rr := RankingResponse{
			ResponseType:   ResponseType{Type: "ranking"},
			Ranking:        makeRegularRanking(),
			LongRanking:    makeLongRanking(),
			UrlRanking:     makeUrlRegularRanking(),
			UrlLongRanking: makeUrlLongRanking(),
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
