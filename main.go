package main

// A sample bot sending welcoming messages as answer to "hello" and "good morning" chat messages
//
// Copyright (C) 2023Joas Schilling <coding@schilljs.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/viper"
)

var (
	config            *viper.Viper
	errInvalidBody    = errors.New("Invalid body supplied")
	letterBytes       = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	possibleResponses = []string{
		"Done!",
	}
	triggerMessageRegex = regexp.MustCompile("^@ha\\s\\w+\\s\\w+")
)

type MessageActor struct {
	Type string `json:"type"`
	Id   string `json:"id"`
	Name string `json:"name"`
}

type MessageObject struct {
	Type      string `json:"type"`
	Id        string `json:"id"`
	Name      string `json:"name"`
	Content   string `json:"content"`
	MediaType string `json:"mediaType"`
}

type MessageTarget struct {
	Type string `json:"type"`
	Id   string `json:"id"`
	Name string `json:"name"`
}

type Message struct {
	Type   string        `json:"type"`
	Actor  MessageActor  `json:"actor"`
	Object MessageObject `json:"object"`
	Target MessageTarget `json:"target"`
}

type Response struct {
	Message string `json:"message"`
	ReplyTo string `json:"replyTo"`
}

type RichObjectParameter struct {
	Id   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type RichObjectMessage struct {
	Message string `json:"message"`
}

type RichObjectMessageWithParameters struct {
	RichObjectMessage
	Parameters map[string]RichObjectParameter `json:"parameters,omitempty"`
}

func createMessage(input string) (Message, error) {
	var message Message
	reader := strings.NewReader(input)
	decoder := json.NewDecoder(reader)
	err := decoder.Decode(&message)
	if err != nil {
		return message, errInvalidBody
	}

	return message, nil
}

func createRichMessage(input string) (RichObjectMessageWithParameters, error) {
	var message RichObjectMessageWithParameters
	reader := strings.NewReader(input)
	decoder := json.NewDecoder(reader)
	err := decoder.Decode(&message)
	if err != nil {
		return message, errInvalidBody
	}

	return message, nil
}

func createRichMessageWithoutParameters(input string) (RichObjectMessage, error) {
	var message RichObjectMessage
	reader := strings.NewReader(input)
	decoder := json.NewDecoder(reader)
	err := decoder.Decode(&message)
	if err != nil {
		return message, errInvalidBody
	}

	return message, nil
}

func generateRandomBytes(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = letterBytes[rand.Intn(len(letterBytes))]
	}
	return string(b)
}

func getRandomResponse() string {
	return possibleResponses[rand.Intn(len(possibleResponses))]
}

func generateHmacForString(message string, random string, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(random + message))
	sum := h.Sum(nil)
	return hex.EncodeToString(sum)
}

func sendReply(server string, message Message, responseText string) {
	random := generateRandomBytes(64)
	signature := generateHmacForString(responseText, random, config.GetString("bot.secret"))

	// Send actual message
	response := Response{
		Message: responseText,
		ReplyTo: message.Object.Id,
	}
	responseBody, _ := json.Marshal(response)
	bodyReader := bytes.NewReader(responseBody)

	requestURL := fmt.Sprintf("%socs/v2.php/apps/spreed/api/v1/bot/%s/message", server, message.Target.Id)
	request, err := http.NewRequest("POST", requestURL, bodyReader)
	if err != nil {
		log.Printf("[Response]      Error creating request %v", err)
		os.Exit(1)
	}

	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("OCS-APIRequest", "true")
	request.Header.Set("X-Nextcloud-Talk-Bot-Random", random)
	request.Header.Set("X-Nextcloud-Talk-Bot-Signature", signature)

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
	}

	_, err = client.Do(request)
	if err != nil {
		log.Printf("[Response]      Error posting request %v", err)
		return
	}
}

func messageHandling(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		// Only post allowed
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("[Request]       Error reading body: %v", err)
		http.Error(w, "can't read body", http.StatusBadRequest)
		return
	}

	server := r.Header.Get("X-NEXTCLOUD-TALK-BACKEND")
	random := r.Header.Get("X-NEXTCLOUD-TALK-RANDOM")
	signature := r.Header.Get("X-NEXTCLOUD-TALK-SIGNATURE")
	digest := generateHmacForString(string(body), random, config.GetString("bot.secret"))

	if digest != signature {
		log.Printf("[Request]       Error validating signature: %s / %s", digest, signature)
		http.Error(w, "Invalid signature", http.StatusBadRequest)
		return
	}

	message, err := createMessage(string(body))

	if err != nil {
		log.Printf("[Request]       Error invalid body: %s", err)
		http.Error(w, "Invalid signature", http.StatusBadRequest)
		return
	}

	if message.Object.Name == "message" {
		richMessage, err := createRichMessageWithoutParameters(message.Object.Content)
		if err == nil {
			if triggerMessageRegex.Match([]byte(richMessage.Message)) {
				log.Printf("[Talk]          Command found: %s", richMessage.Message)

				// Format data
				json := commandToJson(richMessage.Message)

				// Call Home Assistant endpoint
				if callWebhook(json) {
					sendReply(server, message, getRandomResponse())
				} else {
					sendReply(server, message, "Error calling Home Assistant")
				}

			} else {
				log.Printf("[Talk]          Message is not command: %s", richMessage.Message)
			}
		}
	}

	http.Error(w, "Received", http.StatusOK)
}

func callWebhook(jsonData []byte) bool {
	// Remove trailing slashes from ha_url
	cleanedURL := strings.TrimRight(config.GetString("bot.ha.url"), "/")

	// Build the request URL
	url := cleanedURL + "/api/webhook/" + config.GetString("bot.ha.webhook_id")

	// Send the POST request with the JSON data
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("[Webhook]       POST request failed: %s", err)
		return false
	}
	defer resp.Body.Close()

	// Check the response
	if resp.StatusCode == http.StatusOK {
		log.Println("[Webhook]       POST request was successful!")
		// You can read the response body if needed
		// responseBody, _ := ioutil.ReadAll(resp.Body)
		// fmt.Println("Response content:", string(responseBody))

		return true
	} else {
		log.Printf("[Webhook]       POST request failed with status code: %s", strconv.Itoa(resp.StatusCode))
		// You can read the response body if needed
		// responseBody, _ := ioutil.ReadAll(resp.Body)
		// fmt.Println("Response content:", string(responseBody))
	}

	return false
}

func commandToJson(command string) []byte {
	// Split the string into words using whitespace as the delimiter
	words := strings.Fields(command)

	// Check if there are at least two words in the slice
	if len(words) >= 3 {
		// Define the JSON data with the variables
		jsonStr := []byte(fmt.Sprintf(`{
			"action": "%s",
			"target": "%s"
		}`, words[1], words[2]))

		return jsonStr
	} else {
		log.Println("[Talk]          Command doesn't contain at least two words.")
	}

	return nil
}

func main() {
	config = viper.New()
	config.SetConfigName("config")
	config.AddConfigPath(".")
	if err := config.ReadInConfig(); err != nil {
		log.Fatalf("Fatal error config file: %s \n", err)
		return
	}
	log.Println("[Config]        File loaded")

	// Create a mux for routing incoming requests
	m := http.NewServeMux()

	// All URLs will be handled by this function
	m.HandleFunc("/message", messageHandling)

	s := &http.Server{
		Addr:    ":" + config.GetString("bot.port"),
		Handler: m,
	}

	log.Printf("[Network]       Listening on port %d", config.GetInt("bot.port"))
	log.Println("[Network]       Starting to listen and serve")
	log.Fatal(s.ListenAndServe())
}
