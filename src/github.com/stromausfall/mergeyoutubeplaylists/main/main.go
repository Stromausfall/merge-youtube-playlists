package main

import (
	"code.google.com/p/google-api-go-client/youtube/v3"
	"fmt"
	"github.com/stromausfall/mergeyoutubeplaylists/authorization"
	"html/template"
	"net/http"
	"strings"
	"sync"
	"time"
)

func main() {
	// this could be improved...
	cfg := authorization.Config{
		Installed: authorization.ClientConfig{
			// the clientId --> from the google developer project authorization oauth2
			ClientID:     "--- client id ---",
			// the clientId --> from the google developer project authorization oauth2
			ClientSecret: "--- client secret ---",
			RedirectURIs: []string{"http://localhost:80", "urn:ietf:wg:oauth:2.0:oob"},
			AuthURI:      "https://accounts.google.com/o/oauth2/auth",
			TokenURI:     "https://accounts.google.com/o/oauth2/token",
		},
	}

	// requests the user to enter her google account and allow
	// that this app can modify youtube playlists etc...
	// a webpage will be opened for this
	client, err := authorization.BuildOAuthHTTPClient(youtube.YoutubeScope, &cfg)
	checkErr(err, "problem with authorization/authentication")

	// get the youtube service
	service, err := youtube.New(client)
	checkErr(err, "Problem with creating the youtube client")

	// install handlers
	go http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		playLists := collectAllPlayLists(service)
		printPage(playLists, w)
	})
	go http.HandleFunc("/merge", func(w http.ResponseWriter, r *http.Request) {
		handleMerge(w, r, service)
	})

	authorization.OpenURL("http://localhost:80")

	// start web server
	http.ListenAndServe(":80", nil)
}

func checkErr(err error, info string) {
	if err != nil {
		fmt.Println(info, " - ", err, " = ", err.Error())
	}
}

func collectPlaylistVideos(service *youtube.Service, playListIds []string) chan *youtube.ResourceId {
	allVideosChan := make(chan *youtube.ResourceId)
	workerWaitGroup := sync.WaitGroup{}

	for _, currentPlayListId := range playListIds {
		playListId := currentPlayListId
		workerWaitGroup.Add(1)

		go func() {
			defer workerWaitGroup.Done()
			nextPageToken := ""

			for {
				call := service.PlaylistItems.List("snippet").PlaylistId(playListId).PageToken(nextPageToken).MaxResults(50)

				response, err := call.Do()
				checkErr(err, "Error making API call to list videos of channel")

				for _, item := range response.Items {
					allVideosChan <- item.Snippet.ResourceId
				}

				nextPageToken = response.NextPageToken

				if nextPageToken == "" {
					break
				}
			}
			fmt.Println("retrieved videos for playlist : ", playListId, "\n")
		}()
	}

	// spawn the worker that will close the channel after all workers have finished
	go func() {
		workerWaitGroup.Wait()
		close(allVideosChan)
	}()

	return allVideosChan
}

func createNewPlaylist(service *youtube.Service) (playListId, playListTitle string) {
	// create a new playlist
	newPlayList := youtube.Playlist{}
	newPlayList.Snippet = &youtube.PlaylistSnippet{}
	newPlayList.Snippet.Title = "merged playlist @" + time.Now().Format(time.RFC850)
	call := service.Playlists.Insert("snippet", &newPlayList)

	response, err := call.Do()
	checkErr(err, "Error making API call to create a new playlist")

	playListId = response.Id
	playListTitle = newPlayList.Snippet.Title
	return
}

func handleMerge(w http.ResponseWriter, r *http.Request, service *youtube.Service) {
	// we only need this line on the localhost - google app engine doesn't need it ?!
	r.ParseMultipartForm(15485760)

	// collect the playlists that should be merged
	playLists := make([]string, 0)
	for form := range r.Form {
		if strings.HasPrefix(form, "selected-playlist#") {
			playLists = append(playLists, r.FormValue(form))
		}
	}

	// create a new playlist
	createdPlaylistId, playListTitle := createNewPlaylist(service)

	// collect all videos of the new playlist
	newVideosChannel := collectPlaylistVideos(service, playLists)

	workerWaitGroup := sync.WaitGroup{}
	videoCount := 0

	// now fill the playlist
	// --> this can happen concurrently to retrieving the playlists !
	for video := range newVideosChannel {
		videoCount += 1
		videoToAdd := video
		workerWaitGroup.Add(1)

		// this loop is costly - use goroutines to speed it up as good as possible
		go func() {
			defer workerWaitGroup.Done()

			newPlayListItem := youtube.PlaylistItem{}
			newPlayListItem.Snippet = &youtube.PlaylistItemSnippet{}
			newPlayListItem.Snippet.PlaylistId = createdPlaylistId
			newPlayListItem.Snippet.ResourceId = videoToAdd
			call := service.PlaylistItems.Insert("snippet", &newPlayListItem)

			_, err := call.Do()
			checkErr(err, "Error making API call to add a video to the new playlist")

			fmt.Println("added video : ", videoToAdd.VideoId, "\n")
		}()
	}

	workerWaitGroup.Wait()
	fmt.Fprintf(w, "finished moving %v videos to new playlist : %v", videoCount, playListTitle)
}

const selectionForm = `
<html>
  <body>
    <form action="/merge" method="post">
	
        <fieldset> 
{{range $index, $results :=  .}}
          <label for="id-{{$index}}">
            <input type="checkbox" name="selected-playlist#{{$index}}" value="{{.Id}}" id="id-{{$index}}">
            {{.ChannelName}} - {{.PlayListTitle}} ({{.VideosCount}} videos)</br>
          </label> 
{{end}}
        </fieldset> 
      <div><input type="submit" value="Merge playlists"></div>
    </form>
  </body>
</html>
`

type PlayList struct {
	ChannelName   string
	PlayListTitle string
	VideosCount   int64
	Id            string
}

func collectAllPlayLists(service *youtube.Service) *[]PlayList {
	nextPageToken := ""
	playLists := make([]PlayList, 0)

	for {
		call := service.Playlists.List("contentDetails,id,player,snippet,status").Mine(true).PageToken(nextPageToken).MaxResults(50)

		response, err := call.Do()
		checkErr(err, "Error making API call to list channels")

		for _, playlist := range response.Items {
			element := PlayList{}
			element.ChannelName = playlist.Snippet.ChannelTitle
			element.PlayListTitle = playlist.Snippet.Title
			element.VideosCount = playlist.ContentDetails.ItemCount
			element.Id = playlist.Id

			playLists = append(playLists, element)
		}

		nextPageToken = response.NextPageToken

		if nextPageToken == "" {
			break
		}
	}

	return &playLists
}

func printPage(playLists *[]PlayList, w http.ResponseWriter) {
	t1 := template.New("Selection template")
	t3, err := t1.Parse(selectionForm)
	checkErr(err, "problem when parsing template")

	err = t3.Execute(w, playLists)
	checkErr(err, "problem when executing template")
}
