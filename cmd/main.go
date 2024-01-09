package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"

	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus"
	"github.com/joho/godotenv"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	eventsReceived = promauto.NewCounter(prometheus.CounterOpts{
		Name: "events_received_total",
		Help: "The total number of events received",
	})
	eventsForwarded = promauto.NewCounter(prometheus.CounterOpts{
		Name: "events_forwarded_total",
		Help: "The total number of events forwarded successfuly",
	})
	eventsError = promauto.NewCounter(prometheus.CounterOpts{
		Name: "events_forward_error_total",
		Help: "The total number of events that ended in error on forward",
	})
)

func main() {
	log.SetOutput(os.Stdout)

	err := godotenv.Load(".env")
	if err != nil && !os.IsNotExist(err) {
		log.Fatalf("Error loading .env file: %s", err)
	}

	count := 5
	ctx := context.Background()

	connectionString, ok := os.LookupEnv("AZURE_SERVICEBUS_CONNECTION_STRING")
	if !ok {
		log.Fatal("AZURE_SERVICEBUS_CONNECTION_STRING environment variable not found")
	}
	re := regexp.MustCompile(`sb:\/\/([^;\/]+)`)
	match := re.FindStringSubmatch(connectionString)

	queueName, ok := os.LookupEnv("AZURE_SERVICEBUS_QUEUE_NAME")
	if !ok {
		log.Fatal("AZURE_SERVICEBUS_QUEUE_NAME environment variable not found")
	}

	webhookUrl, ok := os.LookupEnv("WEBHOOK_URL")
	if !ok {
		log.Fatal("WEBHOOK_URL environment variable not found")
	}

	httpListen, ok := os.LookupEnv("HTTP_LISTEN")
	if !ok {
		log.Fatal("HTTP_LISTEN environment variable not found")
	}

	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "OK") })
	go func() {
		log.Printf("start HTTP server on %s", httpListen)
		err = http.ListenAndServe(httpListen, nil)
		if err != nil {
			log.Fatalf("error starting http server: %s", err)
		}
	}()

	log.Printf("Connect to service bus: %s\n", match[1])
	client, err := azservicebus.NewClientFromConnectionString(connectionString, nil)
	if err != nil {
		log.Fatalf("Error connection to service bus: %s", err)
	}

	receiver, err := client.NewReceiverForQueue(queueName, nil)
	if err != nil {
		log.Panicf("Error creating receiver: %s", err)
	}
	defer receiver.Close(ctx)

	for {
		messages, err := receiver.ReceiveMessages(ctx, count, nil)
		if err != nil {
			log.Panicf("Error rerieving messages: %s", err)
		}

		log.Printf("Retrieved %v messages\n", len(messages))
		eventsReceived.Add(float64(len(messages)))
		for _, message := range messages {
			body := message.Body
			func() {
				r, err := http.NewRequest("POST", webhookUrl, bytes.NewBuffer(body))
				if err != nil {
					log.Printf("Error creating request: %s", err)
					eventsError.Inc()
					return
				}

				r.Header.Add("Content-Type", "application/json")

				client := &http.Client{}
				res, err := client.Do(r)
				if err != nil {
					log.Printf("Error posting to webhook: %s", err)
					eventsError.Inc()
					return
				}
				defer res.Body.Close()

				if res.StatusCode != http.StatusOK {
					log.Printf("Error POST to %s returned status code: %v", webhookUrl, res.StatusCode)
					eventsError.Inc()
					return
				}
			}()
			err = receiver.CompleteMessage(ctx, message, nil)
			if err != nil {
				log.Fatalf("Error completing messages: %s", err)
			}
			eventsForwarded.Inc()
		}
	}
}
