package main

import (
	"log"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/ghodss/yaml"
	"github.com/labstack/echo"
	"github.com/labstack/echo/middleware"
	"github.com/nlopes/slack"
)

type Event struct {
	APIAppID    string     `json:"api_app_id"`
	AuthedUsers []string   `json:"authed_users"`
	Challenge   string     `json:"challenge"`
	Event       *slack.Msg `json:"event"`
	EventID     string     `json:"event_id"`
	EventTime   uint       `json:"event_time"`
	TeamID      string     `json:"team_id"`
	Token       string     `json:"token"`
	Type        string     `json:"type"`
}

type InstanceCache struct {
	UpdatedAt time.Time
	Instances *ec2.DescribeInstancesOutput
}

var (
	api           *slack.Client
	instanceCache InstanceCache

	interval time.Duration

	slackAccessToken = os.Getenv("SLACK_ACCESS_TOKEN")
	slackVerifyToken = os.Getenv("SLACK_VERIFY_TOKEN")

	hostIDPattern         = regexp.MustCompile("i-[0-9a-f]{5,}")
	privateDnsNamePattern = regexp.MustCompile("ip-[0-9-]+.[a-z]{2}-[a-z]+-[0-9]+.compute.internal")
)

func init() {
	var err error
	interval, err = time.ParseDuration(os.Getenv("INSTANCE_CACHE_TTL"))
	if err != nil {
		log.Println("cannot parse $INSTANE_CACHE_TTL, use default '5m'")
		interval = 5 * time.Minute
	}
}

func main() {
	api = slack.New(slackAccessToken)
	logger := log.New(os.Stdout, "slack-bot: ", log.Lshortfile|log.LstdFlags)
	slack.SetLogger(logger)
	api.SetDebug(true)

	username, err := getUsername()
	if err != nil {
		log.Fatal(err)
	}

	e := echo.New()
	e.Use(middleware.Logger())
	e.Use(middleware.BodyDump(func(c echo.Context, reqBody, resBody []byte) {
		log.Println(string(resBody))
	}))

	e.POST("/", func(c echo.Context) error {
		ev := new(Event)
		if err := c.Bind(ev); err != nil {
			log.Println(err)
			return err
		}

		if ev.Token != slackVerifyToken {
			log.Println("failed to verify token:", ev.Token)
			return c.String(http.StatusUnauthorized, "failed to verify token")
		}

		if ev.Type == "url_verification" {
			return c.String(http.StatusOK, ev.Challenge)
		}

		if ev.Event.Username == username {
			return c.String(http.StatusOK, "ignore own post")
		}

		instances, err := ev.findInstances()
		if err != nil {
			log.Println(err)
			return err
		}
		if len(instances) > 0 {
			for _, i := range instances {
				ev.postInstance(i)
			}
			return c.String(http.StatusOK, "post instance details")
		}

		return c.String(http.StatusOK, "query not found")
	})

	e.Logger.Fatal(e.Start(":3000"))
}

func getUsername() (string, error) {
	resp, err := api.AuthTest()
	if err != nil {
		return "", err
	}
	return resp.User, err
}

func getInstance(query string) (*ec2.Instance, error) {
	svc := ec2.New(session.New())

	var (
		resp *ec2.DescribeInstancesOutput
		err  error
	)
	if instanceCache.UpdatedAt.Add(interval).Before(time.Now()) {
		resp, err = svc.DescribeInstances(nil)
		if err != nil {
			return nil, err
		}
		instanceCache = InstanceCache{
			UpdatedAt: time.Now(),
			Instances: resp,
		}
	} else {
		resp = instanceCache.Instances
	}

	for _, reservation := range resp.Reservations {
		for _, instance := range reservation.Instances {
			if instance.PrivateDnsName != nil && *instance.PrivateDnsName == query {
				return instance, nil
			}
			if instance.InstanceId != nil && *instance.InstanceId == query {
				return instance, nil
			}
		}
	}

	return nil, nil
}

func (ev *Event) findQuery(pattern *regexp.Regexp) []string {
	queries := make(map[string]struct{})
	for _, s := range pattern.FindAllString(ev.Event.Text, -1) {
		queries[s] = struct{}{}
	}
	for _, a := range ev.Event.Attachments {
		for _, s := range pattern.FindAllString(a.Text, -1) {
			queries[s] = struct{}{}
		}
		for _, s := range pattern.FindAllString(a.Title, -1) {
			queries[s] = struct{}{}
		}
		for _, f := range a.Fields {
			for _, s := range pattern.FindAllString(f.Value, -1) {
				queries[s] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(queries))
	for q, _ := range queries {
		result = append(result, q)
	}
	return result
}

func (ev *Event) findInstanceQueries() []string {
	return append(
		ev.findQuery(hostIDPattern),
		ev.findQuery(privateDnsNamePattern)...,
	)
}

func (ev *Event) findInstances() (result []*ec2.Instance, err error) {
	queries := ev.findInstanceQueries()
	if len(queries) == 0 {
		return
	}
	instances := make(map[string]*ec2.Instance)
	notFound := make([]string, 0)
	for _, q := range queries {
		instance, err := getInstance(q)
		if err != nil {
			return nil, err
		}
		if instance == nil {
			notFound = append(notFound, q)
			continue
		}
		instances[*instance.InstanceId] = instance
	}
	if len(notFound) > 0 {
		defer ev.postNoInstance(notFound)
	}
	result = make([]*ec2.Instance, 0, len(instances))
	for _, i := range instances {
		result = append(result, i)
	}
	return
}

func (ev *Event) postInstance(instance *ec2.Instance) error {
	yamlInstance, err := yaml.Marshal(instance)
	if err != nil {
		log.Println(err)
		return err
	}

	tagFields := make([]slack.AttachmentField, len(instance.Tags))
	for i, tag := range instance.Tags {
		tagFields[i] = slack.AttachmentField{
			Title: *tag.Key,
			Value: *tag.Value,
		}
	}

	_, _, err = api.PostMessage(
		ev.Event.Channel,
		*instance.InstanceId,
		slack.PostMessageParameters{
			Attachments: []slack.Attachment{
				slack.Attachment{
					Fields: []slack.AttachmentField{
						slack.AttachmentField{
							Title: "Instance ID",
							Value: *instance.InstanceId,
						},
						slack.AttachmentField{
							Title: "Instance Type",
							Value: *instance.InstanceType,
						},
						slack.AttachmentField{
							Title: "Private DNS Name",
							Value: *instance.PrivateDnsName,
						},
						slack.AttachmentField{
							Title: "Private IP Address",
							Value: *instance.PrivateIpAddress,
						},
						slack.AttachmentField{
							Title: "Public DNS Name",
							Value: *instance.PublicDnsName,
						},
						slack.AttachmentField{
							Title: "Public IP Address",
							Value: *instance.PublicIpAddress,
						},
						slack.AttachmentField{
							Title: "State",
							Value: *instance.State.Name,
						},
					},
				},
				slack.Attachment{
					Title:  "Tags",
					Fields: tagFields,
				},
				slack.Attachment{
					Title: "Details",
					Text:  string(yamlInstance),
				},
			},
			ThreadTimestamp: ev.Event.Timestamp,
		},
	)
	return err
}

func (ev *Event) postNoInstance(queries []string) error {
	a := make([]slack.Attachment, len(queries))
	for i, q := range queries {
		a[i] = slack.Attachment{
			Text:  q,
			Color: "#daa038",
		}
	}
	_, _, err := api.PostMessage(
		ev.Event.Channel,
		"failed to get instance",
		slack.PostMessageParameters{
			Attachments:     a,
			ThreadTimestamp: ev.Event.Timestamp,
		},
	)
	return err
}
