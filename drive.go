package main

import (
	"context"
	"fmt"
	"net/http"

	gdrive "google.golang.org/api/drive/v2"

	"time"

	"strings"

	. "github.com/claudetech/loggo/default"
	"golang.org/x/oauth2"
)

// BlackListObjects is a list of blacklisted items that will not be
// fetched from cache or the API
var BlackListObjects map[string]bool

// init initializes the global configurations
func init() {
	BlackListObjects = make(map[string]bool)
	BlackListObjects[".git"] = true
	BlackListObjects["HEAD"] = true
	BlackListObjects[".Trash"] = true
	BlackListObjects[".Trash-1000"] = true
}

// Drive holds the Google Drive API connection(s)
type Drive struct {
	cache   *Cache
	context context.Context
	token   *oauth2.Token
	config  *oauth2.Config
}

// NewDriveClient creates a new Google Drive client
func NewDriveClient(config *Config, cache *Cache, refreshInterval time.Duration) (*Drive, error) {
	drive := Drive{
		cache:   cache,
		context: context.Background(),
		config: &oauth2.Config{
			ClientID:     config.ClientID,
			ClientSecret: config.ClientSecret,
			Endpoint: oauth2.Endpoint{
				AuthURL:  "https://accounts.google.com/o/oauth2/auth",
				TokenURL: "https://accounts.google.com/o/oauth2/token",
			},
			RedirectURL: "urn:ietf:wg:oauth:2.0:oob",
			Scopes:      []string{gdrive.DriveScope},
		},
	}

	if err := drive.authorize(); nil != err {
		return nil, err
	}

	go drive.startWatchChanges(refreshInterval)

	return &drive, nil
}

func (d *Drive) startWatchChanges(refreshInterval time.Duration) {
	client, err := d.getClient()
	if nil != err {
		Log.Debugf("%v", err)
		Log.Warningf("Could not get Google Drive client to watch for changes")
		return
	}

	checkChanges := func(firstCheck bool) {
		Log.Debugf("Checking for changes")

		changeID, err := d.cache.GetLargestChangeID()
		if nil != err {
			Log.Debugf("%v", err)
			Log.Warningf("Could not get largest change ID")
			return
		}

		if firstCheck {
			Log.Infof("First cache build process started...")
		}

		deletedItems := 0
		updatedItems := 0
		processedItems := 0
		pageToken := ""
		largestChangeID := changeID
		for {
			query := client.Changes.List().IncludeDeleted(true).MaxResults(1000)

			if "" != pageToken {
				query = query.PageToken(pageToken)
			}

			if 0 != changeID {
				query = query.StartChangeId(changeID)
			}

			results, err := query.Do()
			if nil != err {
				Log.Debugf("%v", err)
				Log.Warningf("Could not get changes")
				break
			}

			for _, change := range results.Items {
				Log.Tracef("Change %v", change)

				if change.Deleted || (nil != change.File && change.File.ExplicitlyTrashed) {
					d.cache.DeleteObject(change.FileId)
					deletedItems++
				} else {
					object, err := d.mapFileToObject(change.File)
					if nil != err {
						Log.Debugf("%v", err)
						Log.Warningf("Could not map Google Drive file to object")
					} else {
						err := d.cache.UpdateObject(object)
						if nil != err {
							Log.Debugf("%v", err)
							Log.Warningf("Could not update object %v", object.ObjectID)
						}
						updatedItems++
					}
				}

				processedItems++
			}

			if processedItems > 0 {
				Log.Infof("Processed %v items / deleted %v items / updated %v items",
					processedItems, deletedItems, updatedItems)
			}

			largestChangeID = results.LargestChangeId
			pageToken = results.NextPageToken
			if "" == pageToken {
				break
			}
		}

		if largestChangeID >= changeID {
			d.cache.StoreLargestChangeID(largestChangeID + 1)
		}

		if firstCheck {
			Log.Infof("First cache build process finished!")
		}
	}

	checkChanges(true)
	for _ = range time.Tick(refreshInterval) {
		checkChanges(false)
	}
}

func (d *Drive) authorize() error {
	Log.Debugf("Authorizing against Google Drive API")

	token, err := d.cache.LoadToken()
	if nil != err {
		Log.Debugf("Token could not be found, fetching new one")

		t, err := getTokenFromWeb(d.config)
		if nil != err {
			return err
		}
		token = t
		if err := d.cache.StoreToken(token); nil != err {
			return err
		}
	}

	d.token = token
	return nil
}

// getTokenFromWeb uses Config to request a Token.
// It returns the retrieved Token.
func getTokenFromWeb(config *oauth2.Config) (*oauth2.Token, error) {
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
	fmt.Printf("Go to the following link in your browser %v\n", authURL)
	fmt.Printf("Paste the authorization code: ")

	var code string
	if _, err := fmt.Scan(&code); err != nil {
		return nil, fmt.Errorf("Unable to read authorization code %v", err)
	}

	tok, err := config.Exchange(oauth2.NoContext, code)
	if err != nil {
		return nil, fmt.Errorf("Unable to retrieve token from web %v", err)
	}
	return tok, err
}

// getClient gets a new Google Drive client
func (d *Drive) getClient() (*gdrive.Service, error) {
	return gdrive.New(d.config.Client(d.context, d.token))
}

// getNativeClient gets a native http client
func (d *Drive) getNativeClient() *http.Client {
	return oauth2.NewClient(d.context, d.config.TokenSource(d.context, d.token))
}

// GetRoot gets the root node directly from the API
func (d *Drive) GetRoot() (*APIObject, error) {
	Log.Debugf("Getting root from API")
	id := "root"

	client, err := d.getClient()
	if nil != err {
		Log.Debugf("%v", err)
		return nil, fmt.Errorf("Could not get Google Drive client")
	}

	file, err := client.Files.Get(id).Do()
	if nil != err {
		Log.Debugf("%v", err)
		return nil, fmt.Errorf("Could not get object %v from API", id)
	}

	// getting file size
	if file.MimeType != "application/vnd.google-apps.folder" && 0 == file.FileSize {
		res, err := client.Files.Get(id).Download()
		if nil != err {
			Log.Debugf("%v", err)
			return nil, fmt.Errorf("Could not get file size for object %v", id)
		}
		file.FileSize = res.ContentLength
	}

	return d.mapFileToObject(file)
}

// GetObject gets an object by id
func (d *Drive) GetObject(id string) (*APIObject, error) {
	return d.cache.GetObject(id)
}

// GetObjectsByParent get all objects under parent id
func (d *Drive) GetObjectsByParent(parent string) ([]*APIObject, error) {
	return d.cache.GetObjectsByParent(parent)
}

// GetObjectByParentAndName finds a child element by name and its parent id
func (d *Drive) GetObjectByParentAndName(parent, name string) (*APIObject, error) {
	if _, exists := BlackListObjects[name]; exists {
		return nil, fmt.Errorf("Object %v is blacklisted and will not be returned", name)
	}

	return d.cache.GetObjectByParentAndName(parent, name)
}

// Open a file
func (d *Drive) Open(object *APIObject) (*Buffer, error) {
	nativeClient := d.getNativeClient()
	return GetBufferInstance(nativeClient, object)
}

// mapFileToObject maps a Google Drive file to APIObject
func (d *Drive) mapFileToObject(file *gdrive.File) (*APIObject, error) {
	lastModified, err := time.Parse(time.RFC3339, file.ModifiedDate)
	if nil != err {
		Log.Debugf("%v", err)
		return nil, fmt.Errorf("Could not parse last modified date")
	}

	var parents []string
	for _, parent := range file.Parents {
		parents = append(parents, parent.Id)
	}

	return &APIObject{
		ObjectID:     file.Id,
		Name:         file.Title,
		IsDir:        file.MimeType == "application/vnd.google-apps.folder",
		LastModified: lastModified,
		Size:         uint64(file.FileSize),
		DownloadURL:  file.DownloadUrl,
		Parents:      fmt.Sprintf("|%v|", strings.Join(parents, "|")),
	}, nil
}
