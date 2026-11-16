package desktop

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"strings"
	"time"
)

// One window at a time (#616): a launcher clicked twice, or `mur app` typed while the app is open,
// brings the open window forward rather than opening a second one. The window listens on a
// loopback port and records it, with its process and a key, in a file in the app's directory;
// a later `mur app` that finds the window there asks it to come forward and stops. The key keeps
// any other local program from making the window jump, which is all the port can do.

type instance struct {
	PID  int    `json:"pid"`
	Addr string `json:"addr"`
	Key  string `json:"key"`
}

const showPrefix = "show "

// listenInstance opens the window's port and records it at path. raise is called, from the
// port's goroutine, each time a later `mur app` asks. stop closes the port and takes the record out.
func listenInstance(path string, raise func()) (stop func(), err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	k := make([]byte, 16)
	rand.Read(k)
	rec := instance{PID: os.Getpid(), Addr: ln.Addr().String(), Key: hex.EncodeToString(k)}
	b, _ := json.Marshal(rec)
	if err := os.WriteFile(path+".tmp", b, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		ln.Close()
		return nil, err
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				c.SetDeadline(time.Now().Add(2 * time.Second))
				line, _ := bufio.NewReader(c).ReadString('\n')
				if strings.TrimSpace(line) != showPrefix+rec.Key {
					return
				}
				raise()
				c.Write([]byte("ok\n"))
			}()
		}
	}()
	return func() {
		ln.Close()
		// Only this window's record goes: a second window started meanwhile may have written its own.
		if b, err := os.ReadFile(path); err == nil {
			var now instance
			if json.Unmarshal(b, &now) == nil && now == rec {
				os.Remove(path)
			}
		}
	}, nil
}

// BringForward asks the window recorded at path, if one is open, to come forward, and says
// whether it did.
func BringForward(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var rec instance
	if json.Unmarshal(b, &rec) != nil || rec.Addr == "" || rec.Key == "" {
		return false
	}
	c, err := net.DialTimeout("tcp", rec.Addr, time.Second)
	if err != nil {
		return false
	}
	defer c.Close()
	allowForeground(rec.PID)
	c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Write([]byte(showPrefix + rec.Key + "\n")); err != nil {
		return false
	}
	reply, _ := bufio.NewReader(c).ReadString('\n')
	return strings.TrimSpace(reply) == "ok"
}
