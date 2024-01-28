package main

import (
    "context"
    "encoding/json"
    "fmt"
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promhttp"
    "io"

    log "github.com/sirupsen/logrus"
    "net/http"
    "net/url"
    "os"
    "slices"
    "time"
)

const (
    jiraRequestTimeout         = 30 * time.Second
    jiraTimeFormat             = "2006-01-02T15:04:05.000-0700"
    dayHours           float64 = 24
    weekHours          float64 = 7 * dayHours
    monthHours         float64 = 30.41 * dayHours
    yearHours          float64 = 12 * monthHours
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

// Define Prometheus metrics
var (
    jiraIssueCount = prometheus.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "jira_issue_count",
            Help: "Count of Jira issues by various labels.",
        },
        []string{"project", "priority", "status", "statusCategory", "assignee", "issueType"},
    )
    jiraIssueTimeInStatus = prometheus.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "jira_issue_time_in_status_hours",
            Help:    "Time spent by issues in each status.",
            Buckets: []float64{1, dayHours, 2 * dayHours, 4 * dayHours, weekHours, 2 * weekHours, monthHours, 2 * monthHours, 4 * monthHours, yearHours, 2 * yearHours},
        },
        []string{"project", "priority", "assignee", "issueType", "status"},
    )
)

func init() {
    // Register metrics with Prometheus
    prometheus.MustRegister(jiraIssueCount)
    prometheus.MustRegister(jiraIssueTimeInStatus)
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

    // Repeat every cfg.dataRefreshPeriod and fetch Jira data
    go func() {
        for {
            now := time.Now()
            issues, err := fetchJiraData(cfg)
            if err != nil {
                fmt.Println("Error fetching Jira data:", err)
                return
            }
            log.Infof("Fetched %d issues in %s", len(issues), time.Since(now))
            now = time.Now()
            jiraIssueCount.Reset()
            jiraIssueTimeInStatus.Reset()
            for _, issue := range issues {
                transformDataForPrometheus(issue)
            }
            log.Infof("Metrics updated in %s", time.Since(now))
            time.Sleep(cfg.dataRefreshPeriod)
        }
    }()

    http.Handle("/liveness", livenessHandler())
    http.Handle("/readiness", readinessHandler(cfg))
    http.Handle("/metrics", promhttp.Handler())

    log.Infof("Serving metrics on %s\n", cfg.listen)
    if err := http.ListenAndServe(cfg.listen, nil); err != nil {
        fmt.Println("Error starting HTTP server:", err)
    }
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

type MySelfInfo struct {
    EmailAddress string `json:"emailAddress"`
    Active       bool   `json:"active"`
}

func fetchMyself(ctx context.Context, cfg config) (*MySelfInfo, error) {
    apiURL := fmt.Sprintf("%s/rest/api/3/myself", cfg.jiraURL)
    log.Debugf("Fetch Jira status: %s\n", apiURL)
    myself := new(MySelfInfo)
    if err := request(ctx, cfg, apiURL, myself); err != nil {
        return nil, fmt.Errorf("failed to fetch self info: %w", err)
    }
    return myself, nil
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

    if err := json.NewDecoder(resp.Body).Decode(&target); err != nil {
        return err
    }

    return nil
}

// transformDataForPrometheus updates Prometheus metrics instead of returning a string
func transformDataForPrometheus(issue JiraIssue) {
    //fmt.Printf("Processing issue %s\n", issue.Key)
    jiraIssueCount.With(prometheus.Labels{
        "project":        issue.Fields.Project.Key,
        "priority":       issue.Fields.Priority.Name,
        "status":         issue.Fields.Status.Name,
        "statusCategory": issue.Fields.Status.StatusCategory.Name,
        "assignee":       issue.Fields.Assignee.EmailAddress,
        "issueType":      issue.Fields.IssueType.Name,
    }).Inc()
    calculateStatusDurations(issue)
}

func calculateStatusDurations(issue JiraIssue) {
    type labelsInfo struct {
        status         string
        statusCategory string
    }
    statusDurations := make(map[labelsInfo]time.Duration)

    slices.Reverse(issue.Changelog.Histories)
    statusChangeTime := mustTimeParse(issue.Fields.Created)
    for _, history := range issue.Changelog.Histories {
        changeTime := mustTimeParse(history.Created)
        for _, item := range history.Items {
            if item.Field == "status" {
                duration := changeTime.Sub(statusChangeTime)
                labels := labelsInfo{
                    status: item.FromString.(string),
                }
                statusDurations[labels] += duration
                statusChangeTime = changeTime
            }
        }
    }
    for labels, duration := range statusDurations {
        //fmt.Printf("Issue %s spent %s in status %s\n", issue.Key, duration, status)
        jiraIssueTimeInStatus.With(prometheus.Labels{
            "project":   issue.Fields.Project.Key,
            "priority":  issue.Fields.Priority.Name,
            "assignee":  issue.Fields.Assignee.EmailAddress,
            "issueType": issue.Fields.IssueType.Name,
            "status":    labels.status,
        }).Observe(duration.Hours())
    }
}

func livenessHandler() http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    })
}

func readinessHandler(cfg config) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        log.Infof("readinessHandler")
        _, err := fetchMyself(context.TODO(), cfg)
        if err != nil {
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
