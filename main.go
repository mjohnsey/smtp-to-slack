package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"

	"net/http"
	"os"
	"regexp"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/bluele/slack"
	"github.com/joho/godotenv"
	"github.com/kelseyhightower/envconfig"
	data "github.com/mailhog/data"
)

type MailHogReader struct {
	Host       string
	Port       int
	HttpClient *http.Client
}

func (m MailHogReader) NewReader(host string, port int) MailHogReader {
	client := http.Client{
		Timeout: time.Second * 10,
	}
	return MailHogReader{HttpClient: &client, Host: host, Port: port}
}

func (m *MailHogReader) BaseUrl() string {
	return fmt.Sprintf("http://%s:%d", m.Host, m.Port)
}

func (m *MailHogReader) BaseV1ApiUrl() string {
	return fmt.Sprintf("%s/%s", m.BaseUrl(), "api/v1")
}

func (m *MailHogReader) EventStreamUrl() string {
	return fmt.Sprintf("%s/%s", m.BaseV1ApiUrl(), "events")
}

type MessagesResult struct {
	Total int            `json:"total"`
	Count int            `json:"count"`
	Start int            `json:"start"`
	Items []data.Message `json:"items"`
}

func (m *MailHogReader) GetMessagesRequest() (*http.Request, error) {
	url := fmt.Sprintf("%s/%s/%s", m.BaseUrl(), "api/v2", "messages")
	return http.NewRequest(http.MethodGet, url, nil)
}

func (m *MailHogReader) GetMessages() (*MessagesResult, error) {
	req, reqErr := m.GetMessagesRequest()
	if reqErr != nil {
		log.Fatalln(reqErr)
	}
	res, getErr := m.HttpClient.Do(req)
	if getErr != nil {
		log.Fatalln(getErr)
	}
	body, readErr := ioutil.ReadAll(res.Body)
	if readErr != nil {
		log.Fatal(readErr)
	}
	// fmt.Println(body)
	var messages MessagesResult
	jsonErr := json.Unmarshal(body, &messages)
	if jsonErr != nil {
		log.Fatalln(jsonErr)
	}
	return &messages, nil
}

func (mr MessagesResult) FileNameFromDisposition(disposition []string) (filename string) {
	if len(disposition) > 0 {
		r := regexp.MustCompile(`(?m)^attachment;\s*filename="?([^"]+)"?$`)
		if len(disposition) == 1 {
			disp := disposition[0]
			if r.MatchString(disp) {
				submatches := r.FindAllStringSubmatch(disp, -1)
				if len(submatches) > 0 {
					if len(submatches[0]) > 0 {
						filename = submatches[0][1]
					}
				}
			}
		}
	}
	return
}

type Attachment struct {
	Filename string
	Content  []byte
}

func (mr MessagesResult) GetAttachments(msg data.Message) (*[]Attachment, error) {
	var attachments []Attachment
	for _, part := range msg.MIME.Parts {
		disposition := part.Headers["Content-Disposition"]
		filename := mr.FileNameFromDisposition(disposition)
		if filename != "" {
			log.Println(filename)
			data, err := base64.StdEncoding.DecodeString(part.Body)
			if err != nil {
				return nil, err
			}
			a := Attachment{Filename: filename, Content: data}
			attachments = append(attachments, a)
		}
	}
	return &attachments, nil
}

func SendAttachToSlack(api *slack.Slack, channelId string, attach Attachment) (*slack.UploadedFile, error) {
	tmpFile, tmpErr := ioutil.TempFile("", "smtp_attachment")
	if tmpErr != nil {
		return nil, tmpErr
	}
	defer os.Remove(tmpFile.Name()) // clean up these temp files
	bytes := attach.Content

	if _, err := tmpFile.Write(bytes); err != nil {
		return nil, err
	}
	if err := tmpFile.Close(); err != nil {
		return nil, err
	}
	log.Println("Writing: ", tmpFile.Name())
	info, err := api.FilesUpload(&slack.FilesUploadOpt{
		Filepath: tmpFile.Name(),
		Filetype: "auto",
		Filename: attach.Filename,
		Title:    attach.Filename,
		Channels: []string{channelId},
	})
	if err != nil {
		return nil, err
	}
	return info, nil
}

type MessageRelayConfig struct {
	SlackToken       string `envconfig:"SLACK_TOKEN"`        // TODO: add required
	Host             string `envconfig:"MAILHOG_HOST"`       // TODO: add default
	Port             int    `envconfig:"MAILHOG_PORT"`       // TODO: add default
	SlackChannelName string `envconfig:"SLACK_CHANNEL_NAME"` // TODO: add required
}

// EnvPrefix is the prefix used by the ENV variables for this program
func (config MessageRelayConfig) EnvPrefix() string {
	return "MESSAGE_RELAY"
}

func main() {
	dotEnvErr := godotenv.Load()
	if dotEnvErr != nil {
		log.Fatalln(dotEnvErr)
	}
	log.Println("hello")
	var spec MessageRelayConfig
	err := envconfig.Process(MessageRelayConfig{}.EnvPrefix(), &spec)

	if err != nil {
		log.Fatalln(err)
	}

	mh := MailHogReader{}.NewReader(spec.Host, spec.Port)
	log.Println("Calling: ", mh.BaseUrl())
	messages, err := mh.GetMessages()
	if err != nil {
		log.Fatalln(err)
	}
	api := slack.New(spec.SlackToken)
	channel, err := api.FindChannelByName(spec.SlackChannelName)
	if err != nil {
		log.Fatalln(err)
	}
	for msgNum, msg := range messages.Items {
		log.Println("Message #", msgNum)
		attaches, err := messages.GetAttachments(msg)
		if err != nil {
			log.Fatalln(err)
		}
		for _, attach := range *attaches {
			info, err := SendAttachToSlack(api, channel.Id, attach)
			if err != nil {
				log.Fatalln(err)
			}
			log.Println(info.PrivateDownloadUrl)
		}
		log.Println("You can delete Message #", msgNum)
	}
}
