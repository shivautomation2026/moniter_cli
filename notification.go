package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
)

func sendDiscordNotification(webhookURL, message string, logger *log.Logger, clientName string) {
	payload := map[string]string{
		"content": message,
	}
	b, _ := json.Marshal(payload)

	resp, err := http.Post(webhookURL, "application/json", strings.NewReader(string(b)))
	if err != nil {
		logger.Printf("%s Failed to send Discord notification: %v", clientName, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		logger.Printf("%s Discord notification failed with status %d: %s", clientName, resp.StatusCode, string(body))
	} else {
		logger.Printf("%s Discord notification sent successfully.", clientName)
	}
}
