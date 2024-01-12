package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
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

	token, _ := os.LookupEnv("HTTP_BEARER_TOKEN")
	caFile, _ := os.LookupEnv("TLS_CA_FILE")
	certFile, _ := os.LookupEnv("TLS_CERT_FILE")
	keyFile, _ := os.LookupEnv("TLS_KEY_FILE")

	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "OK") })
	http.HandleFunc("/post", func(w http.ResponseWriter, r *http.Request) {
		authorization := r.Header.Get("Authorization")
		if token != "" && authorization != ("Bearer "+token) {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte("403 - Invalid bearer token"))
			log.Println("error invalid authorization header sent")
		} else if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			w.Write([]byte("405 - Method not allowed"))
			log.Printf("error invalid method used: %s\n", r.Method)
		} else {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				log.Printf("Error reading POST body: %s\n", err)
			}
			log.Printf("received post body:\n%s\n", body)
		}
		fmt.Fprint(w, "OK")
	})
	go func() {
		if certFile != "" && keyFile != "" {
			log.Printf("start HTTPS server on %s", httpListen)
			err = http.ListenAndServeTLS(httpListen, certFile, keyFile, nil)
		} else {
			log.Printf("start HTTP server on %s", httpListen)
			err = http.ListenAndServe(httpListen, nil)
		}
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

	certPool, err := x509.SystemCertPool()
	if err != nil {
		log.Panicf("Error loading system cert pool: %s", err)
	}
	if caFile != "" {
		if caCertPEM, err := os.ReadFile(caFile); err != nil {
			log.Panicf("Error reading file '%s': %s", caFile, err)
		} else if ok := certPool.AppendCertsFromPEM(caCertPEM); !ok {
			log.Panicf("Error invalid PEM certificate: %s", caFile)
		}
	}
	tlsConfig := &tls.Config{
		RootCAs: certPool,
	}
	tr := &http.Transport{
		TLSClientConfig: tlsConfig,
	}

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
				if ok {
					r.Header.Add("Authorization", "Bearer "+token)
				}

				client := &http.Client{Transport: tr}
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
