package command

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/lintrieu/nefertiti/errors"
	"github.com/lintrieu/nefertiti/flag"
)

type (
	ListenCommand struct {
		*CommandMeta
	}
)

var (
	chCallback chan int64
)

func (c *ListenCommand) Run(args []string) int {
	var err error

	router := mux.NewRouter()
	router.HandleFunc("/ping", ping).Host("127.0.0.1").Methods(http.MethodGet)
	router.HandleFunc("/post", post).Host("127.0.0.1").Methods(http.MethodPost)
	router.HandleFunc("/", delete).Host("127.0.0.1").Methods(http.MethodDelete)
	router.HandleFunc("/callback", callback).Host("127.0.0.1").Methods(http.MethodPost)

	port := flag.Get("port")
	if port.Exists {
		if *c.Port, err = port.Int64(); err != nil {
			return c.ReturnError(err)
		}
		log.Printf("[INFO] Listening to port %d...", *c.Port)
		err = http.ListenAndServe(fmt.Sprintf(":%d", *c.Port), router)
		if err != nil {
			return c.ReturnError(err)
		}
	} else {
		for {
			log.Printf("[INFO] Listening to port %d...", *c.Port)
			err = http.ListenAndServe(fmt.Sprintf(":%d", *c.Port), router)
			if err == nil {
				break
			} else {
				if strings.Contains(err.Error(), "address already in use") {
					*c.Port++
				} else {
					return c.ReturnError(err)
				}
			}
		}
	}

	return 0
}

func (c *ListenCommand) Help() string {
	return "Usage: ./nefertiti listen [options]"
}

func (c *ListenCommand) Synopsis() string {
	return "Starts a server listening to the specified port."
}

//----------------------------------- Pong ------------------------------------

type Pong struct {
	Port    int64    `json:"port"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

type Pongs []Pong

func (pongs Pongs) indexByPort(port int64) int {
	for i, pong := range pongs {
		if pong.Port == port {
			return i
		}
	}
	return -1
}

func (pongs Pongs) pongByPort(port int64) *Pong {
	i := pongs.indexByPort(port)
	if i != -1 {
		return &pongs[i]
	}
	return nil
}

//----------------------------------- misc ------------------------------------

func getHostPort(req *http.Request) (host string, port int64, err error) {
	colon := strings.LastIndexByte(req.Host, ':')
	if colon == -1 {
		return req.Host, 38700, nil
	} else {
		host = req.Host[:colon]
		port, err = strconv.ParseInt(req.Host[colon+1:], 10, 64)
		return
	}
}

func getFirstAvailablePort(req *http.Request) int64 {
	host, port, _ := getHostPort(req)
	for { // enumerate over ports, starting with "our" port + 1
		port++
		_, err := http.Get("http://" + host + ":" + strconv.FormatInt(port, 10) + "/ping")
		if err != nil {
			break
		}
	}
	return port
}

// GET 127.0.0.1:[port]/ping

func getPong(req *http.Request) Pongs {
	out := Pongs{}

	host, port, err := getHostPort(req)
	if err != nil {
		log.Printf("[ERROR] %v", err)
		return out
	}

	for { // enumerate over ports, starting with "our" port + 1
		port++
		resp, err := http.Get("http://" + host + ":" + strconv.FormatInt(port, 10) + "/ping")
		if err != nil {
			break
		}
		// add the response to this func result
		if raw, err := ioutil.ReadAll(resp.Body); err != nil {
			log.Printf("[ERROR] %v", err)
		} else {
			var pong Pong
			if err := json.Unmarshal(raw, &pong); err != nil {
				log.Printf("[ERROR] %v", err)
			} else {
				out = append(out, pong)
			}
		}
	}

	return out
}

func ping(resp http.ResponseWriter, req *http.Request) {
	json.NewEncoder(resp).Encode(getPong(req))
}

// POST 127.0.0.1:[port]/post

func returnError(resp http.ResponseWriter, err error) {
	http.Error(resp, err.Error(), http.StatusNotFound)
}

func returnPortErr(resp http.ResponseWriter, port int64) {
	http.Error(resp, fmt.Sprintf("Port %d is invalid or did not answer", port), http.StatusNotFound)
}

func post(resp http.ResponseWriter, req *http.Request) {
	host, self, err := getHostPort(req)
	if err != nil {
		returnError(resp, err)
		return
	}

	req.ParseForm()

	// did the request include a port number?
	for key, value := range req.Form {
		if key == "port" {
			port, err := strconv.ParseInt(value[0], 10, 64)
			if err != nil {
				returnError(resp, err)
				return
			}
			if port == self {
				returnPortErr(resp, port)
				return
			}
			pong := getPong(req).pongByPort(port)
			if pong == nil {
				returnPortErr(resp, port)
				return
			}
			// create the POST request
			post, err := http.NewRequest("POST", fmt.Sprintf("http://%s:%d/post", host, port), strings.NewReader(req.Form.Encode()))
			if err != nil {
				returnError(resp, err)
				return
			}
			post.Header.Add("Content-Type", "application/x-www-form-urlencoded")
			// submit the POST request
			if _, err := http.DefaultClient.Do(post); err != nil {
				returnError(resp, err)
				return
			}
			json.NewEncoder(resp).Encode(getPong(req))
			return
		}
	}

	// no port number? start a new instance
	port := getFirstAvailablePort(req)

	command := req.Form.Get("command")
	if command == "" {
		returnError(resp, errors.New("missing argument: command"))
		return
	}

	args := append(
		[]string{command, "--listen"},
		fmt.Sprintf("--hub=%d", self),
		fmt.Sprintf("--port=%d", port),
	)
	for key, value := range req.Form {
		if key != "" && key != "port" && key != "command" {
			arg := "--" + key
			if value != nil {
				if value[0] != "" {
					arg = arg + "=" + value[0]
				}
			}
			args = append(args, arg)
		}
	}

	cmd := exec.Command(os.Args[0], args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if chCallback == nil {
		chCallback = make(chan int64)
	} else {
	L: // empty the channel
		for {
			select {
			case <-chCallback:
			default:
				break L
			}
		}
	}

	// start new cmd in a goroutine, because if it runs then it does not return
	chStdError := make(chan string)
	go func() {
		err := cmd.Run()
		if err == nil {
			// this will happen after a DELETE, and that is why we empty the channel above
			chCallback <- port
		} else {
			if _, ok := err.(*exec.ExitError); ok {
				chStdError <- stderr.String()
			} else {
				chStdError <- err.Error()
			}
		}
	}()

	// let us wait for 30 seconds. if the cmd returns, then it probably errored out. otherwise, assume that it started without an error.
	select {
	case result := <-chStdError:
		returnError(resp, errors.New(result))
		return
	case result := <-chCallback:
		if result == port {
			break
		}
	case <-time.After(30 * time.Second):
		break
	}

	json.NewEncoder(resp).Encode(getPong(req))
}

// POST 127.0.0.1:[port]/callback

func callback(resp http.ResponseWriter, req *http.Request) {
	if chCallback != nil {
		req.ParseForm()
		port := req.Form.Get("port")
		if port != "" {
			port, err := strconv.ParseInt(port, 10, 64)
			if err == nil {
				chCallback <- port
			}
		}
	}
}

// DELETE 127.0.0.1:[port]

func delete(resp http.ResponseWriter, req *http.Request) {
	host, _, err := getHostPort(req)
	if err != nil {
		returnError(resp, err)
		return
	}

	req.ParseForm()

	port := req.Form.Get("port")
	if port != "" {
		port, err := strconv.ParseInt(port, 10, 64)
		if err != nil {
			returnError(resp, err)
			return
		}
		delete, err := http.NewRequest("DELETE", fmt.Sprintf("http://%s:%d/", host, port), nil)
		if err != nil {
			returnError(resp, err)
			return
		}
		http.DefaultClient.Do(delete)
		json.NewEncoder(resp).Encode(getPong(req))
		return
	}

	pongs := getPong(req)
	for _, pong := range pongs {
		delete, err := http.NewRequest("DELETE", fmt.Sprintf("http://%s:%d/", host, pong.Port), nil)
		if err != nil {
			returnError(resp, err)
			return
		}
		http.DefaultClient.Do(delete)
	}

	resp.Write([]byte(""))
	defer os.Exit(0)
}
