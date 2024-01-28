package main

import (
    "context"
    "encoding/json"
    "fmt"
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promhttp"
    log "github.com/sirupsen/logrus"
    "io"
    "net/http"
    "net/url"
    "os"
    "slices"
    "time"
)

const (
    jiraRequestTimeout = 30 * time.Second
    jiraTimeFormat     = "2006-01-02T15:04:05.000-0700"
)

type config struct {
    listen            string
    dataRefreshPeriod time.Duration
    jiraURL           string
    jiraUser          string
    jiraAPIToken      string
    projects          string
    analyzePeriodDays string
}

type statusMap map[string]string

// Define Prometheus metrics
var (
    jiraIssueCount = prometheus.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "jira_issue_count",
            Help: "Count of Jira issues by various labels.",
        },
        []string{"project", "priority", "status", "statusCategory", "assignee", "issueType"},
    )
    jiraIssueHoursInStatusCount = prometheus.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "jira_issue_hours_in_status_count",
            Help: "Time spent by issues in each status.",
        },
        []string{"project", "priority", "status", "statusCategory", "assignee", "issueType"},
    )
)

func init() {
    // Register metrics with Prometheus
    prometheus.MustRegister(jiraIssueCount)
    prometheus.MustRegister(jiraIssueHoursInStatusCount)
}

func main() {
    log.SetOutput(os.Stderr)
    log.SetReportCaller(false)
    //log.SetLevel(log.InfoLevel)
    log.SetLevel(log.DebugLevel)
    log.SetFormatter(
        &log.TextFormatter{
            TimestampFormat: "15:04:05",
            FullTimestamp:   true,
            ForceColors:     true,
        },
    )

    var err error
    cfg := config{
        listen:            getEnvOrDie("LISTEN"),
        analyzePeriodDays: getEnvOrDefault("ANALYZE_PERIOD_DAYS", "90"),
        jiraURL:           getEnvOrDie("JIRA_URL"),
        jiraUser:          getEnvOrDie("JIRA_USER"),
        jiraAPIToken:      getEnvOrDie("JIRA_API_TOKEN"),
        projects:          getEnvOrDie("PROJECTS"),
    }
    cfg.dataRefreshPeriod, err = time.ParseDuration(getEnvOrDefault("DATA_REFRESH_PERIOD", "5m"))
    failOnError(err)
    if cfg.analyzePeriodDays == "" {
        cfg.analyzePeriodDays = "90"
    }

    http.Handle("/liveness", livenessHandler())
    http.Handle("/readiness", readinessHandler(cfg))
    http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
        if err := updateMetrics(cfg); err != nil {
            log.Fatalf("failed to update metrics: %s", err)
        }
        h := promhttp.HandlerFor(prometheus.DefaultGatherer, promhttp.HandlerOpts{})
        h.ServeHTTP(w, r)
    })

    log.Infof("Serving metrics on %s\n", cfg.listen)
    if err := http.ListenAndServe(cfg.listen, nil); err != nil {
        fmt.Println("Error starting HTTP server:", err)
    }
}

func updateMetrics(cfg config) error {
    now := time.Now()
    statusToCategory := make(statusMap)
    if err := buildStatusMap(cfg, statusToCategory); err != nil {
        return fmt.Errorf("failed to build status map: %w", err)
    }
    log.Infof("Status map built in %s", time.Since(now))

    now = time.Now()
    issues, err := fetchJiraData(cfg)
    if err != nil {
        return fmt.Errorf("failed to fetch Jira data: %w", err)
    }
    log.Infof("Fetched %d issues in %s", len(issues), time.Since(now))

    now = time.Now()
    jiraIssueCount.Reset()
    jiraIssueHoursInStatusCount.Reset()
    for _, issue := range issues {
        if err := transformDataForPrometheus(statusToCategory, issue); err != nil {
            return fmt.Errorf("failed to transform data for Prometheus: %w", err)
        }
    }
    log.Infof("Metrics updated in %s", time.Since(now))

    return nil
}

func buildStatusMap(cfg config, sm statusMap) error {
    log.Infof("Fetching statuses from Jira")
    type respStruct struct {
        Name           string `json:"name"`
        ID             string `json:"id"`
        StatusCategory struct {
            ID   int    `json:"id"`
            Name string `json:"name"`
        } `json:"statusCategory"`
    }
    apiURL := fmt.Sprintf("%s/rest/api/3/status", cfg.jiraURL)
    log.Debugf("Fetch Jira statuses: %s\n", apiURL)
    statuses := make([]respStruct, 0)
    if err := request(context.TODO(), cfg, apiURL, &statuses); err != nil {
        return fmt.Errorf("failed to fetch statuses: %w", err)
    }
    log.Infof("Fetched %d statuses", len(statuses))
    for _, status := range statuses {
        sm[status.Name] = status.StatusCategory.Name
    }
    return nil
}

// fetchJiraData connects to the Jira API and fetches issues data
func fetchJiraData(cfg config) ([]JiraIssue, error) {
    issues := make([]JiraIssue, 0)
    startAt := 0
    for {
        issuesChunk, err := fetchStartingFrom(cfg, startAt)
        if err != nil {
            return nil, err
        }
        if len(issuesChunk) == 0 {
            break
        }
        issues = append(issues, issuesChunk...)
        startAt += len(issuesChunk)
    }
    return issues, nil
}

// JiraIssue represents the structure of an issue from Jira
type JiraIssue struct {
    Key       string `json:"key"`
    Changelog struct {
        Histories []struct {
            Created string `json:"created"`
            Items   []struct {
                Field      string      `json:"field"`
                FromString interface{} `json:"fromString"`
            } `json:"items"`
        } `json:"histories"`
    } `json:"changelog"`
    Fields struct {
        Created  string `json:"created"`
        Priority struct {
            Name string `json:"name"`
        } `json:"priority"`
        Assignee struct {
            EmailAddress string `json:"emailAddress"`
        } `json:"assignee"`
        Status struct {
            Name           string `json:"name"`
            StatusCategory struct {
                Name string `json:"name"`
            } `json:"statusCategory"`
        } `json:"status"`
        IssueType struct {
            Name string `json:"name"`
        } `json:"issuetype"`
        Project struct {
            Key string `json:"key"`
        } `json:"project"`
    } `json:"fields"`
}

func fetchStartingFrom(cfg config, startAt int) ([]JiraIssue, error) {
    log.Debugf("Fetching Jira data starting from %d", startAt)
    // Adjust the API URL based on your Jira setup
    jql := fmt.Sprintf("updated >= -%sd AND project in (%s)", cfg.analyzePeriodDays, cfg.projects)
    apiURL := fmt.Sprintf("%s/rest/api/3/search?expand=changelog&fields=created,status,assignee,project,issuetype&startAt=%d&jql=%s", cfg.jiraURL, startAt, url.QueryEscape(jql))
    log.Debugf("Fetching %s", apiURL)

    // Decode the JSON response
    var result struct {
        Issues []JiraIssue `json:"issues"`
    }
    if err := request(context.TODO(), cfg, apiURL, &result); err != nil {
        return result.Issues, fmt.Errorf("failed to fetch issues: %w", err)
    }
    return result.Issues, nil
}

func testMyselfEndpoint(ctx context.Context, cfg config) error {
    apiURL := fmt.Sprintf("%s/rest/api/3/myself", cfg.jiraURL)
    log.Debugf("Fetch Jira status: %s\n", apiURL)
    resp := new(interface{})
    if err := request(ctx, cfg, apiURL, resp); err != nil {
        return fmt.Errorf("failed to fetch self info: %w", err)
    }
    return nil
}

func request(ctx context.Context, cfg config, apiURL string, target interface{}) error {
    ctx, cancel := context.WithTimeout(ctx, jiraRequestTimeout)
    defer cancel()
    // Create a new HTTP request
    req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
    if err != nil {
        return err
    }

    // Set authentication headers
    req.SetBasicAuth(cfg.jiraUser, cfg.jiraAPIToken)

    // Make the HTTP request
    client := &http.Client{}
    resp, err := client.Do(req)
    if err != nil {
        return err
    }
    defer func(Body io.ReadCloser) { _ = Body.Close() }(resp.Body)

    // Check if the response is successful
    if resp.StatusCode != http.StatusOK {
        return fmt.Errorf("failed to fetch data: %s", resp.Status)
    }

    //body, _ := io.ReadAll(resp.Body)
    //log.Debugf("Response: %s\n", string(body))

    if err := json.NewDecoder(resp.Body).Decode(&target); err != nil {
        return err
    }

    return nil
}

// transformDataForPrometheus updates Prometheus metrics instead of returning a string
func transformDataForPrometheus(statusToCategory statusMap, issue JiraIssue) error {
    //fmt.Printf("Processing issue %s\n", issue.Key)
    jiraIssueCount.With(prometheus.Labels{
        "project":        issue.Fields.Project.Key,
        "priority":       issue.Fields.Priority.Name,
        "status":         issue.Fields.Status.Name,
        "statusCategory": issue.Fields.Status.StatusCategory.Name,
        "assignee":       issue.Fields.Assignee.EmailAddress,
        "issueType":      issue.Fields.IssueType.Name,
    }).Inc()
    statusDurations := make(map[string]time.Duration)
    slices.Reverse(issue.Changelog.Histories)
    statusChangeTime := mustTimeParse(issue.Fields.Created)
    for _, history := range issue.Changelog.Histories {
        changeTime := mustTimeParse(history.Created)
        for _, item := range history.Items {
            if item.Field == "status" {
                duration := changeTime.Sub(statusChangeTime)
                statusDurations[item.FromString.(string)] += duration
                statusChangeTime = changeTime
            }
        }
    }
    for status, duration := range statusDurations {
        cat, exists := statusToCategory[status]
        if !exists {
            return fmt.Errorf("status `%s` not found in status map", status)
        }
        jiraIssueHoursInStatusCount.With(prometheus.Labels{
            "project":        issue.Fields.Project.Key,
            "priority":       issue.Fields.Priority.Name,
            "assignee":       issue.Fields.Assignee.EmailAddress,
            "issueType":      issue.Fields.IssueType.Name,
            "status":         status,
            "statusCategory": cat,
        }).Add(duration.Hours())
    }
    return nil
}

func livenessHandler() http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    })
}

func readinessHandler(cfg config) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        log.Infof("readinessHandler")
        if err := testMyselfEndpoint(context.TODO(), cfg); err != nil {
            fmt.Printf("Error fetching Jira data: %s\n", err)
            w.WriteHeader(http.StatusInternalServerError)
            return
        } else {
            w.WriteHeader(http.StatusOK)
        }
    })
}

func getEnvOrDie(name string) string {
    value := os.Getenv(name)
    if value == "" {
        log.Fatalf("%s env is empty", name)
    }
    return value
}

func getEnvOrDefault(name string, defaultValue string) string {
    value := os.Getenv(name)
    if value == "" {
        return defaultValue
    }
    return value
}

func failOnError(err error) {
    if err != nil {
        log.Fatalf("Error: %s", err)
    }
}

func mustTimeParse(str string) time.Time {
    t, err := time.Parse(jiraTimeFormat, str)
    if err != nil {
        log.Fatal(err)
    }
    return t
}
