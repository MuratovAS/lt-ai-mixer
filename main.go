package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func handleSpecialRequest(w http.ResponseWriter, r *http.Request, client *http.Client) bool {
	if r.URL.Path != "/v2/check" || r.Method != "POST" {
		return false
	}

	// Parse form-data BEFORE checking parameters
	if err := r.ParseForm(); err != nil {
		log.Error().Err(err).Msg("Form parsing error")
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return true
	}

	// Check text and data parameters
	cleanText, foundSpecial := checkSpecialParams(r)

	log.Debug().
		Str("text_param", r.FormValue("text")).
		Str("data_param", r.FormValue("data")).
		Interface("all_params", r.Form).
		Bool("foundSpecial", foundSpecial).
		Msg("Form parameters")

	if cleanText == "" {
		http.Error(w, "Missing 'text' or 'data' parameter", http.StatusBadRequest)
		return true
	}
	if !foundSpecial {
		// For regular requests redirect to proxyRequest
		return false
	}

	responseText := callOpenAI(client, cleanText)
	if responseText == "" {
		return true
	}

	sendAIResponse(w, cleanText, responseText)
	return true
}

func checkSpecialParams(r *http.Request) (string, bool) {
	// Check required parameters
	text := r.FormValue("text")
	data := r.FormValue("data")

	if text == "" && data == "" {
		log.Warn().Msg("Missing required text or data parameters")
		return "", true // Return true for error handling
	}

	// For regular requests (without //ai) just return the text
	if text != "" {
		if !strings.HasSuffix(strings.TrimSpace(text), "//ai") {
			return text, false
		}
		// Handle special request with //ai
		cleanText := text[:strings.LastIndex(text, "//ai")]
		return cleanText, true
	}

	// Handle data parameter
	if data != "" {
		var jsonData struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(data), &jsonData); err == nil && jsonData.Text != "" {
			if !strings.HasSuffix(strings.TrimSpace(jsonData.Text), "//ai") {
				return jsonData.Text, false
			}
			// Handle special request with //ai
			cleanText := jsonData.Text[:strings.LastIndex(jsonData.Text, "//ai")]
			return cleanText, true
		}
	}

	return "", false
}

func callOpenAI(client *http.Client, prompt string) string {
	fullPrompt := prompt
	if systemPrompt := os.Getenv("OPENAI_PROMPT"); systemPrompt != "" {
		fullPrompt = systemPrompt + "\n\n" + prompt
	}

	openaiReqBody, _ := json.Marshal(map[string]interface{}{
		"model": os.Getenv("OPENAI_MODEL"),
		"messages": []map[string]string{
			{"role": "user", "content": fullPrompt},
		},
	})

	openaiReq, err := http.NewRequest("POST", os.Getenv("OPENAI_URL")+"/chat/completions", bytes.NewBuffer(openaiReqBody))
	if err != nil {
		log.Error().Err(err).Msg("Error creating OpenAI request")
		return ""
	}

	openaiReq.Header.Set("Content-Type", "application/json")
	openaiReq.Header.Set("Authorization", "Bearer "+os.Getenv("OPENAI_TOKEN"))

	openaiResp, err := client.Do(openaiReq)
	if err != nil {
		log.Error().Err(err).Msg("Error making OpenAI request")
		return ""
	}
	defer openaiResp.Body.Close()

	var openaiResult struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(openaiResp.Body).Decode(&openaiResult); err != nil {
		log.Error().Err(err).Msg("Error decoding OpenAI response")
		return ""
	}

	if len(openaiResult.Choices) > 0 {
		return openaiResult.Choices[0].Message.Content
	}
	return ""
}

func sendAIResponse(w http.ResponseWriter, cleanText, responseText string) {
	response := map[string]interface{}{
		"software": map[string]interface{}{
			"name":       "LT-AI-mixer",
			"apiVersion": 1,
		},
		"language": map[string]interface{}{
			"name": "English",
			"code": "en",
		},
		"matches": []map[string]interface{}{
			{
				"message":      "Reply from LLM",
				"shortMessage": "AI Response",
				"replacements": []map[string]interface{}{
					{"value": responseText},
				},
				"offset": 0,
				"length": len([]rune(cleanText)) + 4,
				"context": map[string]interface{}{
					"text":   cleanText,
					"offset": len([]rune(cleanText)),
					"length": 4,
				},
				"rule": map[string]interface{}{
					"id":          "AI_RESPONSE",
					"description": "Response from AI API",
					"issueType":   "recommendations",
					"category": map[string]interface{}{
						"id":   "AI",
						"name": "AI Responses",
					},
				},
			},
		},
	}

	responseJson, _ := json.MarshalIndent(response, "", "  ")
	log.Debug().RawJSON("language_tool_response", responseJson).Msg("Response in LanguageTool API format")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

func proxyRequest(w http.ResponseWriter, r *http.Request, client *http.Client) {
	// Create new request body with same parameters
	body := strings.NewReader(r.Form.Encode())
	req, err := http.NewRequest(r.Method, os.Getenv("LANGUAGETOOL_URL")+r.URL.Path, body)
	if err != nil {
		log.Error().Err(err).Msg("Error creating request")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Copy headers and set Content-Type
	for name, values := range r.Header {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// Execute request
	resp, err := client.Do(req)
	if err != nil {
		log.Error().Err(err).Msg("Error executing request")
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Copy response headers
	for name, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}

	// Copy status and response body
	w.WriteHeader(resp.StatusCode)
	_, err = io.Copy(w, resp.Body)
	if err != nil {
		log.Error().Err(err).Msg("Error copying response body")
	}
}

func main() {
	port := flag.String("port", "8080", "Server port")
	logLevel := flag.String("log-level", "warn", "Logging level (debug, info, warn, error, fatal, panic)")
	flag.Parse()

	level, err := zerolog.ParseLevel(*logLevel)
	if err != nil {
		level = zerolog.WarnLevel
	}
	zerolog.SetGlobalLevel(level)
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.DateTime}).Level(level)

	client := &http.Client{
		Timeout: 60 * time.Second,
	}

	// Setup handler for all paths
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if handled := handleSpecialRequest(w, r, client); handled {
			return
		}
		proxyRequest(w, r, client)
	})

	// Start server
	log.Debug().Str("port", *port).Msg("LT-AI-mixer started")
	log.Fatal().Err(http.ListenAndServe(":"+*port, nil)).Msg("Server error")
}
