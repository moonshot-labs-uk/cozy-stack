package notes

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/cozy/cozy-stack/model/instance"
	"github.com/cozy/cozy-stack/model/note"
	"github.com/cozy/cozy-stack/pkg/config/config"
	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/realtime"
	"github.com/cozy/cozy-stack/tests/testutils"
	"github.com/cozy/cozy-stack/web/errors"
	webRealtime "github.com/cozy/cozy-stack/web/realtime"
	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
)

var ts *httptest.Server
var inst *instance.Instance
var token string
var noteID string
var version int64

func TestCreateNote(t *testing.T) {
	body := `
{
  "data": {
    "type": "io.cozy.notes.documents",
    "attributes": {
      "title": "A super note",
      "schema": {
        "nodes": [
          ["doc", { "content": "block+" }],
          ["paragraph", { "content": "inline*", "group": "block" }],
          ["blockquote", { "content": "block+", "group": "block" }],
          ["horizontal_rule", { "group": "block" }],
          [
            "heading",
            {
              "content": "inline*",
              "group": "block",
              "attrs": { "level": { "default": 1 } }
            }
          ],
          ["code_block", { "content": "text*", "marks": "", "group": "block" }],
          ["text", { "group": "inline" }],
          [
            "image",
            {
              "group": "inline",
              "inline": true,
              "attrs": { "alt": {}, "src": {}, "title": {} }
            }
          ],
          ["hard_break", { "group": "inline", "inline": true }],
          [
            "ordered_list",
            {
              "content": "list_item+",
              "group": "block",
              "attrs": { "order": { "default": 1 } }
            }
          ],
          ["bullet_list", { "content": "list_item+", "group": "block" }],
          ["list_item", { "content": "paragraph block*" }]
        ],
        "marks": [
          ["link", { "attrs": { "href": {}, "title": {} }, "inclusive": false }],
          ["em", {}],
          ["strong", {}],
          ["code", {}]
        ],
        "topNode": "doc"
      }
    }
  }
}`
	req, _ := http.NewRequest("POST", ts.URL+"/notes", bytes.NewBufferString(body))
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 201, res.StatusCode)
	var result map[string]interface{}
	err = json.NewDecoder(res.Body).Decode(&result)
	assert.NoError(t, err)
	assertInitialNote(t, result)
}

func TestGetNote(t *testing.T) {
	req, _ := http.NewRequest("GET", ts.URL+"/notes/"+noteID, nil)
	req.Header.Add("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	var result map[string]interface{}
	err = json.NewDecoder(res.Body).Decode(&result)
	assert.NoError(t, err)
	assertInitialNote(t, result)
}

func assertInitialNote(t *testing.T, result map[string]interface{}) {
	data, _ := result["data"].(map[string]interface{})
	assert.Equal(t, "io.cozy.files", data["type"])
	if noteID == "" {
		assert.Contains(t, data, "id")
		noteID = data["id"].(string)
	} else {
		assert.Equal(t, noteID, data["id"])
	}
	attrs := data["attributes"].(map[string]interface{})
	assert.Equal(t, "file", attrs["type"])
	assert.Equal(t, "A super note.cozy-note", attrs["name"])
	fcm, _ := attrs["cozyMetadata"].(map[string]interface{})
	assert.Contains(t, fcm, "createdAt")
	assert.Contains(t, fcm, "createdOn")
	meta, _ := attrs["metadata"].(map[string]interface{})
	assert.Equal(t, "A super note", meta["title"])
	assert.EqualValues(t, 0, meta["version"])
	assert.NotNil(t, meta["schema"])
	assert.NotNil(t, meta["content"])
}

func TestChangeTitle(t *testing.T) {
	body := `
{
  "data": {
    "type": "io.cozy.notes.documents",
    "attributes": {
      "title": "A new title"
    }
  }
}`
	req, _ := http.NewRequest("PUT", ts.URL+"/notes/"+noteID+"/title", bytes.NewBufferString(body))
	req.Header.Add("Content-Type", "application/vnd.api+json")
	req.Header.Add("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	var result map[string]interface{}
	err = json.NewDecoder(res.Body).Decode(&result)
	assert.NoError(t, err)

	data, _ := result["data"].(map[string]interface{})
	assert.Equal(t, "io.cozy.files", data["type"])
	assert.Equal(t, noteID, data["id"])
	attrs := data["attributes"].(map[string]interface{})
	assert.Equal(t, "A new title.cozy-note", attrs["name"])
	meta, _ := attrs["metadata"].(map[string]interface{})
	assert.Equal(t, "A new title", meta["title"])
	assert.EqualValues(t, 0, meta["version"])
	assert.NotNil(t, meta["schema"])
	assert.NotNil(t, meta["content"])
}

func TestPatchNote(t *testing.T) {
	body := `{
  "data": [{
    "type": "io.cozy.notes.steps",
    "attributes": {
      "stepType": "replace",
      "from": 1,
      "to": 1,
      "slice": {
        "content": [{ "type": "text", "text": "H" }]
      }
    }
  }, {
    "type": "io.cozy.notes.steps",
    "attributes": {
      "stepType": "replace",
      "from": 2,
      "to": 2,
      "slice": {
        "content": [{ "type": "text", "text": "ello" }]
      }
    }
  }]
}`
	req, _ := http.NewRequest("PATCH", ts.URL+"/notes/"+noteID, bytes.NewBufferString(body))
	req.Header.Add("Content-Type", "application/vnd.api+json")
	req.Header.Add("Authorization", "Bearer "+token)
	req.Header.Add("If-Match", "0")
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	var result map[string]interface{}
	err = json.NewDecoder(res.Body).Decode(&result)
	assert.NoError(t, err)

	data, _ := result["data"].(map[string]interface{})
	assert.Equal(t, "io.cozy.files", data["type"])
	assert.Equal(t, noteID, data["id"])
	attrs := data["attributes"].(map[string]interface{})
	meta, _ := attrs["metadata"].(map[string]interface{})
	v, _ := meta["version"].(float64)
	version = int64(v)
	assert.Greater(t, version, int64(0))
	assert.NotNil(t, meta["schema"])
	assert.NotNil(t, meta["content"])

	req, _ = http.NewRequest("PATCH", ts.URL+"/notes/"+noteID, bytes.NewBufferString(body))
	req.Header.Add("Content-Type", "application/vnd.api+json")
	req.Header.Add("Authorization", "Bearer "+token)
	req.Header.Add("If-Match", "0")
	res, err = http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 409, res.StatusCode)
}

func TestGetSteps(t *testing.T) {
	body := `{
  "data": [{
    "type": "io.cozy.notes.steps",
    "attributes": {
      "stepType": "replace",
      "from": 6,
      "to": 6,
      "slice": {
        "content": [{ "type": "text", "text": " " }]
      }
    }
  }, {
    "type": "io.cozy.notes.steps",
    "attributes": {
      "stepType": "replace",
      "from": 7,
      "to": 7,
      "slice": {
        "content": [{ "type": "text", "text": "world" }]
      }
    }
  }]
}`
	req, _ := http.NewRequest("PATCH", ts.URL+"/notes/"+noteID, bytes.NewBufferString(body))
	req.Header.Add("Content-Type", "application/vnd.api+json")
	req.Header.Add("Authorization", "Bearer "+token)
	req.Header.Add("If-Match", fmt.Sprintf("%d", version))
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 200, res.StatusCode)
	var result map[string]interface{}
	err = json.NewDecoder(res.Body).Decode(&result)
	assert.NoError(t, err)
	data, _ := result["data"].(map[string]interface{})
	attrs := data["attributes"].(map[string]interface{})
	meta, _ := attrs["metadata"].(map[string]interface{})
	last, _ := meta["version"].(float64)
	lastVersion := int64(last)
	assert.Greater(t, lastVersion, int64(0))

	path2 := fmt.Sprintf("/notes/%s/steps?Version=%d", noteID, version)
	req2, _ := http.NewRequest("GET", ts.URL+path2, nil)
	req2.Header.Add("Authorization", "Bearer "+token)
	res2, err := http.DefaultClient.Do(req2)
	assert.NoError(t, err)
	assert.Equal(t, 200, res2.StatusCode)
	var result2 map[string]interface{}
	err = json.NewDecoder(res2.Body).Decode(&result2)
	assert.NoError(t, err)
	meta2, _ := result2["meta"].(map[string]interface{})
	assert.EqualValues(t, 2, meta2["count"])
	data2, _ := result2["data"].([]interface{})
	assert.Len(t, data2, 2)
	first, _ := data2[0].(map[string]interface{})
	assert.NotNil(t, first["id"])
	attrsF, _ := first["attributes"].(map[string]interface{})
	assert.Equal(t, "replace", attrsF["stepType"])
	assert.EqualValues(t, 6, attrsF["from"])
	assert.EqualValues(t, 6, attrsF["to"])
	second, _ := data2[1].(map[string]interface{})
	assert.NotNil(t, second["id"])
	attrsS, _ := second["attributes"].(map[string]interface{})
	assert.Equal(t, "replace", attrsS["stepType"])
	assert.EqualValues(t, 7, attrsS["from"])
	assert.EqualValues(t, 7, attrsS["to"])

	path3 := fmt.Sprintf("/notes/%s/steps?Version=%d", noteID, lastVersion)
	req3, _ := http.NewRequest("GET", ts.URL+path3, nil)
	req3.Header.Add("Authorization", "Bearer "+token)
	res3, err := http.DefaultClient.Do(req3)
	assert.NoError(t, err)
	assert.Equal(t, 200, res3.StatusCode)
	var result3 map[string]interface{}
	err = json.NewDecoder(res3.Body).Decode(&result3)
	assert.NoError(t, err)
	meta3, _ := result3["meta"].(map[string]interface{})
	assert.EqualValues(t, 0, meta3["count"])
	data3, ok := result3["data"].([]interface{})
	assert.True(t, ok)
	assert.Empty(t, data3)
}

func TestPutTelepointer(t *testing.T) {
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		sub := realtime.GetHub().Subscriber(inst)
		sub.Subscribe(consts.NotesEvents)
		wg.Done()
		e := <-sub.Channel
		assert.Equal(t, "UPDATED", e.Verb)
		assert.Equal(t, noteID, e.Doc.ID())
		doc, ok := e.Doc.(note.Event)
		assert.True(t, ok)
		assert.Equal(t, consts.NotesTelepointers, doc["doctype"])
		assert.Equal(t, "543781490137", doc["sessionID"])
		assert.Equal(t, "textSelection", doc["type"])
		assert.EqualValues(t, 7, doc["anchor"])
		assert.EqualValues(t, 12, doc["head"])
		wg.Done()
	}()

	// Wait that the goroutine has subscribed to the realtime
	wg.Wait()
	wg.Add(1)
	body := `{
  "data": {
    "type": "io.cozy.notes.telepointers",
    "attributes": {
      "sessionID": "543781490137",
      "anchor": 7,
      "head": 12,
      "type": "textSelection"
    }
  }
}`
	req, _ := http.NewRequest("PUT", ts.URL+"/notes/"+noteID+"/telepointer", bytes.NewBufferString(body))
	req.Header.Add("Content-Type", "application/vnd.api+json")
	req.Header.Add("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	assert.NoError(t, err)
	assert.Equal(t, 204, res.StatusCode)
	// Wait that the goroutine has received the telepointer update
	wg.Wait()
}

func TestNoteRealtime(t *testing.T) {
	u := strings.Replace(ts.URL+"/realtime/", "http", "ws", 1)
	c, _, err := websocket.DefaultDialer.Dial(u, nil)
	if !assert.NoError(t, err) {
		return
	}
	defer c.Close()

	auth := fmt.Sprintf(`{"method": "AUTH", "payload": "%s"}`, token)
	err = c.WriteMessage(websocket.TextMessage, []byte(auth))
	if !assert.NoError(t, err) {
		return
	}

	msg := `{"method": "SUBSCRIBE", "payload": { "type": "io.cozy.notes.events", "id": "` + noteID + `" }}`
	err = c.WriteMessage(websocket.TextMessage, []byte(msg))
	if !assert.NoError(t, err) {
		return
	}

	// To check that the realtime has made the subscription, we send a fake
	// message and wait for its response.
	msg = `{"method": "PING"}`
	err = c.WriteMessage(websocket.TextMessage, []byte(msg))
	if !assert.NoError(t, err) {
		return
	}
	var res map[string]interface{}
	err = c.ReadJSON(&res)
	assert.NoError(t, err)

	pointer := note.Event{
		"sessionID": "543781490137",
		"anchor":    7,
		"head":      12,
		"type":      "textSelection",
	}
	pointer.SetID(noteID)
	err = note.PutTelepointer(inst, pointer)
	assert.NoError(t, err)
	var res2 map[string]interface{}
	err = c.ReadJSON(&res2)
	assert.NoError(t, err)
	assert.Equal(t, "UPDATED", res2["event"])
	payload2, _ := res2["payload"].(map[string]interface{})
	assert.Equal(t, noteID, payload2["id"])
	assert.Equal(t, "io.cozy.notes.events", payload2["type"])
	doc2, _ := payload2["doc"].(map[string]interface{})
	assert.Equal(t, "io.cozy.notes.telepointers", doc2["doctype"])
	assert.Equal(t, "543781490137", doc2["sessionID"])
	assert.EqualValues(t, 7, doc2["anchor"])
	assert.EqualValues(t, 12, doc2["head"])
	assert.Equal(t, "textSelection", doc2["type"])
}

func TestMain(m *testing.M) {
	config.UseTestFile()
	testutils.NeedCouchdb()
	setup := testutils.NewSetup(m, "notes_test")
	inst = setup.GetTestInstance()
	_, token = setup.GetTestClient(consts.Files)

	ts = setup.GetTestServerMultipleRoutes(map[string]func(*echo.Group){
		"/notes":    Routes,
		"/realtime": webRealtime.Routes,
	})
	ts.Config.Handler.(*echo.Echo).HTTPErrorHandler = errors.ErrorHandler
	os.Exit(setup.Run())
}
