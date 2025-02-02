package releasechecker

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/4itosik/github-releases-notifier/pkg/models"
	"golang.org/x/oauth2"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/shurcooL/graphql"
)

var Github = "github.com"
var Gitlab = "gitlab.com"
var ApiUrl = map[string]string{Github: "https://api.github.com/graphql", Gitlab: "https://gitlab.com/api/graphql"}

// Checker knows about the current repositories releases to compare against.
type Checker struct {
	logger   log.Logger
	tokens   map[string]string
	releases map[string]models.Repository
}

type QueryResult interface {
	GetID() models.ID
	GetName() models.String
	GetDescription() models.String
	GetURL() *url.URL
	GetReleasesCount() int
	GetLatestReleaseID() models.ID
	GetLatestReleaseName() models.String
	GetLatestReleaseDescription() models.String
	GetLatestReleaseURL() *url.URL
	GetLatestReleasePublishingDate() time.Time
}

func NewChecker(logger log.Logger, tokens map[string]string) *Checker {
	return &Checker{
		logger: logger,
		tokens: tokens,
	}
}

// Run the queries and comparisons for the given repositories in a given interval.
func (c *Checker) Run(interval time.Duration, repositories []string, releases chan<- models.Repository) {
	if c.releases == nil {
		c.releases = make(map[string]models.Repository)
	}

	for {
		for _, repoName := range repositories {
			s := strings.Split(repoName, "/")
			website, owner, name := s[0], s[1], s[2]
			level.Debug(c.logger).Log(
				"website", website,
				"owner", owner,
				"name", name,
				"msg", "start crawler",
			)
			var nextRepo models.Repository
			var err error

			if name != "" {
				if website == Github || website == Gitlab {
					nextRepo, err = c.query(website, owner, name)
				} else {
					err = errors.New(website + " is not supported")
				}
			} else {
				err = errors.New("no website specified")
			}

			if err != nil {
				level.Warn(c.logger).Log(
					"msg", "failed to query the repository's releases",
					"owner", owner,
					"name", name,
					"err", err,
				)
				continue
			}

			currRepo, ok := c.releases[repoName]

			// We've queried the repository for the first time.
			// Saving the current state to compare with the next iteration.
			if !ok {
				c.releases[repoName] = nextRepo
				continue
			}
			level.Debug(c.logger).Log(
				"owner", owner,
				"name", name,
				"msg", fmt.Sprintf("Current release published at %+v. Release name: %s", currRepo.Release.PublishedAt, currRepo.Release.Name),
			)

			level.Debug(c.logger).Log(
				"owner", owner,
				"name", name,
				"msg", fmt.Sprintf("Next release published at %+v. Release name: %s", nextRepo.Release.PublishedAt, currRepo.Release.Name),
			)

			if nextRepo.Release.PublishedAt.After(currRepo.Release.PublishedAt) {
				releases <- nextRepo
				c.releases[repoName] = nextRepo
			} else {
				level.Debug(c.logger).Log(
					"msg", "no new release for repository",
					"owner", owner,
					"name", name,
				)
			}
		}
		time.Sleep(interval)
	}
}

func (c *Checker) getClient(website string) (*graphql.Client, error) {
	if token, ok := c.tokens[website]; ok {
		tokenSource := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
		client := oauth2.NewClient(context.Background(), tokenSource)

		return graphql.NewClient(ApiUrl[website], client), nil
	}

	return nil, errors.New("no token for " + website + " available")
}

func (c *Checker) queryGithub(client *graphql.Client, variables map[string]interface{}) (QueryResult, error) {
	query := models.GithubQuery{}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Query(ctx, &query, variables); err != nil {
		return nil, err
	}

	return query, nil
}

func (c *Checker) queryGitlab(client *graphql.Client, variables map[string]interface{}) (QueryResult, error) {
	query := models.GitlabQuery{}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Query(ctx, &query, variables); err != nil {
		return nil, err
	}

	return query, nil
}

func (c *Checker) query(website string, owner string, name string) (models.Repository, error) {
	var client *graphql.Client
	var queryResult QueryResult
	var err error

	client, err = c.getClient(website)
	if err != nil {
		return models.Repository{}, err
	}

	variables := map[string]interface{}{
		"owner": models.String(owner),
		"name":  models.String(name),
	}

	if website == Github {
		queryResult, err = c.queryGithub(client, variables)
	} else if website == Gitlab {
		// variables must be recreated because specifying unused map keys will throw error
		variables = map[string]interface{}{
			"fullPath": owner + "/" + name,
		}

		queryResult, err = c.queryGitlab(client, variables)
	}

	if err != nil {
		return models.Repository{}, err
	}

	repositoryID, ok := queryResult.GetID().(string)
	if !ok {
		return models.Repository{}, fmt.Errorf("can't convert repository id to string: %v", queryResult.GetID())
	}

	if queryResult.GetReleasesCount() == 0 {
		return models.Repository{}, fmt.Errorf("can't find any releases for %s/%s", owner, name)
	}

	releaseID, ok := queryResult.GetLatestReleaseID().(models.String)
	if !ok {
		rID, ok := queryResult.GetLatestReleaseID().(string)

		if !ok {
			return models.Repository{}, fmt.Errorf("can't convert release id to string: %v", queryResult.GetLatestReleaseID())
		} else {
			releaseID = models.String(rID)
		}
	}

	return models.Repository{
		ID:          repositoryID,
		Name:        string(queryResult.GetName()),
		Owner:       owner,
		Description: string(queryResult.GetDescription()),
		URL:         *queryResult.GetURL(),

		Release: models.Release{
			ID:          string(releaseID),
			Name:        string(queryResult.GetLatestReleaseName()),
			Description: string(queryResult.GetLatestReleaseDescription()),
			URL:         *queryResult.GetLatestReleaseURL(),
			PublishedAt: queryResult.GetLatestReleasePublishingDate(),
		},
	}, nil
}
