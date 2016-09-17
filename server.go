package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

var (
	slackTemplate       Slack
	checkRequestChannel chan *Check
	checkConfigs        []Check
)

// Slack config
type Slack struct {
	URL      string `json:"url"`
	Username string `json:"username"`
	Icon     string `json:"icon_emoji"`
	Channel  string `json:"channel"`
	Text     string `json:"text"`
}

// Check is a struct representing info for an http checker
type Check struct {
	ID          string `json:"id"`
	URL         string `json:"url"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Method      string `json:"method"`
	Interval    int64  `json:"interval"`
	Timeout     int64  `json:"timeout"`

	// results
	Timestamp    time.Time `json:"timestamp"`
	StatusCode   int       `json:"statusCode"`
	ResponseTime int64     `json:"responseTime"`
	Error        string    `json:"error"`
	PreviousOK   bool      `json:"previousOk"`
}

// Config is a struct representing data for checkers
type Config struct {
	Slack  Slack   `json:"slack"`
	Checks []Check `json:"checks"`
}

// Broker tracks attached clients and broadcasts events to those clients.
type Broker struct {
	clients     map[chan *Check]bool
	newClients  chan chan *Check
	oldClients  chan chan *Check
	checkResult chan *Check
}

// Start creates a new goroutine, handling addition & removal of clients, and
// broadcasting of checkResult out to clients that are currently attached.
func (broker *Broker) Start() {
	go func() {
		for {
			select {
			case sendChannel := <-broker.newClients:
				broker.clients[sendChannel] = true
			case sendChannel := <-broker.oldClients:
				delete(broker.clients, sendChannel)
				close(sendChannel)
			case check := <-broker.checkResult:
				for sendChannel := range broker.clients {
					sendChannel <- check
				}
			}
		}
	}()
}

func (broker *Broker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
		return
	}

	checkResult := make(chan *Check)
	broker.newClients <- checkResult

	notify := w.(http.CloseNotifier).CloseNotify()
	go func() {
		<-notify
		broker.oldClients <- checkResult
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	for {
		check, open := <-checkResult
		if !open {
			// disconnected client
			break
		}

		stringified, err := json.Marshal(*check)
		if err != nil {
			fmt.Fprint(w, "data: {}\n\n")
		} else {
			fmt.Fprintf(w, "data: %s\n\n", stringified)
		}

		f.Flush()
	}
}

// createCheckRequests makes url request check at the specified interval
func createCheckRequests() {
	for i := range checkConfigs {
		go func(check *Check) {
			ticker := time.NewTicker(time.Duration(check.Interval) * time.Second)
			for {
				<-ticker.C
				checkRequestChannel <- check
			}
		}(&checkConfigs[i])
	}
}

// checkRequestChannelListener listens to checkRequestChannel and performs requests
func checkRequestChannelListener(broker *Broker) {
	for check := range checkRequestChannel {
		go doRequest(check, broker)
	}
}

// doRequest performs check requests
// and sends it to checkResults of Broker
func doRequest(check *Check, broker *Broker) {
	client := http.Client{
		Timeout: time.Duration(check.Timeout) * time.Millisecond,
	}

	request, err := http.NewRequest(check.Method, check.URL, nil)
	if err != nil {
		check.Error = err.Error()
		broker.checkResult <- check
		return
	}

	start := time.Now()
	check.Timestamp = start
	resp, err := client.Do(request)
	if err != nil {
		check.Error = err.Error()
		broker.checkResult <- check

		if err, ok := err.(net.Error); ok && err.Timeout() {
			check.ResponseTime = check.Timeout
			// notify slack only if status changed
			if check.PreviousOK {
				text := fmt.Sprintf("%s [%s] timed out after %dms.", check.Name, check.URL, check.Timeout)
				notifySlack(text)
			}
		} else {
			// notify slack only if status changed
			if check.PreviousOK {
				text := fmt.Sprintf("%s [%s] is down.", check.Name, check.URL)
				notifySlack(text)
			}
		}

		// set PreviousOK for next check
		check.PreviousOK = false

		return
	}

	check.Error = ""
	check.StatusCode = resp.StatusCode
	elapsed := time.Since(start)
	check.ResponseTime = int64(elapsed / time.Millisecond)

	log.Printf("%s %s - %dms - %d %s", check.Method, check.URL, check.ResponseTime, check.StatusCode, check.Error)

	broker.checkResult <- check

	// notify slack if status changed
	if !check.PreviousOK {
		text := fmt.Sprintf("%s [%s] is up.", check.Name, check.URL)
		notifySlack(text)
	}

	// set PreviousOK for next check
	check.PreviousOK = true
}

// notifySlack sends an alert message to slack
func notifySlack(text string) {
	slackTemplate.Text = text
	b := new(bytes.Buffer)
	err := json.NewEncoder(b).Encode(slackTemplate)
	if err != nil {
		return
	}
	if slackTemplate.URL != "" {
		http.Post(slackTemplate.URL, "application/json; charset=utf-8", b)
	}
}

func main() {
	// read from config file
	_, currentFilePath, _, ok := runtime.Caller(0)
	if !ok {
		fmt.Println("Error recovering current file path.")
	}
	absConfigFile := filepath.Join(filepath.Dir(currentFilePath), "static", "config.json")
	configFile, err := os.Open(absConfigFile)
	if err != nil {
		fmt.Println("Error opening config file:\n", err.Error())
		os.Exit(1)
	}

	// parse config file
	jsonParser := json.NewDecoder(configFile)
	var config Config
	if err = jsonParser.Decode(&config); err != nil {
		fmt.Println("Error parsing config file:\n", err.Error())
		os.Exit(1)
	}

	// create slackTemplate
	slackTemplate = config.Slack

	// create buffered channel for check requests
	checkRequestChannel = make(chan *Check, len(config.Checks))

	// create check configurations
	checkConfigs = config.Checks
	for i := range checkConfigs {
		// defaults
		checkConfigs[i].Error = ""
		checkConfigs[i].PreviousOK = true
	}

	// create check requests
	createCheckRequests()

	// make new broker instance
	broker := &Broker{
		make(map[chan *Check]bool),
		make(chan (chan *Check)),
		make(chan (chan *Check)),
		make(chan *Check),
	}
	broker.Start()

	// goroutine that listens in on channel receiving check requests
	go checkRequestChannelListener(broker)

	// set broker as the HTTP handler for /events
	http.Handle("/events", broker)

	// serve static folder
	http.Handle("/", http.FileServer(http.Dir(filepath.Join(filepath.Dir(currentFilePath), "static"))))

	log.Println("Running checks...serving on port 8080.")
	http.ListenAndServe(":8080", nil)
}
