// Package rocketchat posts a plan's messages to a Rocket.Chat channel through an incoming webhook.
package rocketchat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

// ID is the plugin's identifier.
const ID = "rocketchat"

const webhookURL = "webhook_url"

// The incoming webhook's address carries its token: it is a secret, at either scope.
var webhookField = capability.Field{
	Name: webhookURL, Label: "URL du webhook entrant", Type: capability.FieldText, Secret: true,
	Help: "L'adresse que Rocket.Chat donne à une intégration entrante. Elle porte son jeton : " +
		"qui la connaît écrit dans le canal.",
	Placeholder: "https://chat.example.fr/hooks/…",
}

// Plugin describes the plugin.
func Plugin() capability.Plugin {
	return capability.Plugin{
		ID:         ID,
		Version:    "1.0.0",
		APIVersion: sdk.APIVersion,
		Label:      "Rocket.Chat",
		Summary:    "Messages des plans vers un canal Rocket.Chat, par un webhook entrant.",
		Executor:   executor{client: &http.Client{}},
		Actions: []capability.ActionSpec{{
			Suffix: "message", Label: "Message Rocket.Chat",
			Summary: "Envoie un message au canal du webhook entrant.",
			Fields: []capability.ActionField{
				{
					Name: "text", Label: "Message", Required: true, Template: true,
					Help: "Markdown, avec les variables du plan : `{environment}`, `{tag}`, " +
						"`{release.notes}`, `{release.url}`.",
				},
				{
					Name: "channel", Label: "Canal",
					Help: "Vide : celui de l'intégration entrante.", Placeholder: "#livraisons",
				},
			},
		}},
		Global:     []capability.Field{webhookField},
		Project:    []capability.Field{webhookField},
		PerProject: true,
	}
}

type executor struct{ client *http.Client }

func (e executor) Execute(ctx context.Context, target capability.Target, suffix string, params map[string]string) error {
	if suffix != "message" {
		return fmt.Errorf("action %q unknown to plugin %q", suffix, ID)
	}
	address := strings.TrimSpace(target.Settings[webhookURL])
	if address == "" {
		return fmt.Errorf("plugin %q: set the incoming webhook's URL", ID)
	}
	text := strings.TrimSpace(params["text"])
	if text == "" {
		return fmt.Errorf("an empty message is not sent")
	}

	message := map[string]string{"text": text}
	if channel := strings.TrimSpace(params["channel"]); channel != "" {
		message["channel"] = channel
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		return err
	}

	timeout := target.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, address, bytes.NewReader(encoded))
	if err != nil {
		// The address is not quoted: it carries the webhook's token.
		return fmt.Errorf("rocketchat: the webhook's URL is invalid")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := e.client.Do(request)
	if err != nil {
		return fmt.Errorf("rocketchat: sending the message: %w", redacted(err, address))
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("rocketchat: the webhook answered %d", response.StatusCode)
	}
	return nil
}

// redacted keeps the webhook's address, which carries its token, out of an error.
func redacted(err error, address string) error {
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), address, "<webhook>"))
}
