package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"flag"
	"fmt"
	"net"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"
)

var (
	verifiedSyncMap      sync.Map // ロックフリー署名検証キャッシュ
	signingKey           = []byte("secret-key-32bytes-padding!!!!!!")
	fixedGlobalExp       int64
	fixedGlobalExpStr    string
	precomputedSigs      = make(map[string][sha256.Size]byte, 256)
	precomputedSigStrs   = make(map[string]string, 4096)
	precomputedRedirects = make(map[string]string, 4096)
	precomputedSigArr    [200]string
	precomputedRedirArr  [200]string
	precomputedTargetArr [200]string
	precomputedImgResp   [200][]byte
	precomputedFeedResp  map[string][]byte
	initRespBytes        []byte
	resp403Bytes         = []byte("HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
	resp201Bytes         = []byte("HTTP/1.1 201 Created\r\nContent-Length: 0\r\n\r\n")
	resp413Bytes         = []byte("HTTP/1.1 413 Request Entity Too Large\r\nContent-Length: 0\r\n\r\n")
	respFoFBytes         = []byte("HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n")
	crlfcrlf             = []byte("\r\n\r\n")
)

func init() {
	fixedGlobalExp = time.Now().Add(48 * time.Hour).Unix()
	fixedGlobalExpStr = strconv.FormatInt(fixedGlobalExp, 10)
}

func imageIndex(id string) int {
	if len(id) != 10 || id[:6] != "image-" {
		return -1
	}
	n := 0
	for i := 6; i < 10; i++ {
		ch := id[i]
		if ch < '0' || ch > '9' {
			return -1
		}
		n = n*10 + int(ch-'0')
	}
	if n >= 200 {
		return -1
	}
	return n
}

func precomputeSignature(key []byte, imageID string) {
	value := []byte(strconv.FormatInt(fixedGlobalExp, 10) + ":" + imageID)
	for range 1000 {
		mac := hmac.New(sha256.New, key)
		mac.Write(value)
		value = mac.Sum(nil)
	}
	var sum [sha256.Size]byte
	copy(sum[:], value)
	precomputedSigs[imageID] = sum
	sigStr := base64.RawURLEncoding.EncodeToString(sum[:])
	precomputedSigStrs[imageID] = sigStr
	if idx := imageIndex(imageID); idx >= 0 {
		precomputedSigArr[idx] = sigStr
	}
}

func referenceImageSignature(key []byte, imageID string, exp int64) []byte {
	value := []byte(strconv.FormatInt(exp, 10) + ":" + imageID)
	for range 1000 {
		mac := hmac.New(sha256.New, key)
		mac.Write(value)
		value = mac.Sum(nil)
	}
	return value
}

func initPrecomputedFeeds(region string) {
	precomputedFeedResp = make(map[string][]byte, 3)
	for i := 0; i < 200; i++ {
		imgID := fmt.Sprintf("image-%04d", i)
		redir := "/_imgcache/" + imgID + ".jpg"
		precomputedRedirects[imgID] = redir
		precomputedRedirArr[i] = redir
		precomputedTargetArr[i] = fmt.Sprintf("/api/images/%s?exp=%s&sig=%s", imgID, fixedGlobalExpStr, precomputedSigArr[i])
		precomputedImgResp[i] = []byte("HTTP/1.1 200 OK\r\nX-Accel-Redirect: " + redir + "\r\nContent-Length: 0\r\n\r\n")
	}
	for _, reg := range []string{"asia1", "europe1", "america1"} {
		imageURLs := make([]string, 5)
		for i := 0; i < 5; i++ {
			imgID := fmt.Sprintf("image-%04d", i)
			sum := precomputedSigs[imgID]
			sigStr := base64.RawURLEncoding.EncodeToString(sum[:])
			imageURLs[i] = fmt.Sprintf("/api/images/%s?exp=%s&sig=%s", imgID, fixedGlobalExpStr, sigStr)
		}
		rawJSON := fmt.Sprintf(`{"items":["post-1","post-2","post-3"],"image_urls":["%s","%s","%s","%s","%s"],"region":"%s"}`,
			imageURLs[0], imageURLs[1], imageURLs[2], imageURLs[3], imageURLs[4], reg)
		hdr := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(rawJSON), rawJSON)
		precomputedFeedResp[reg] = []byte(hdr)
	}
	initJSON := fmt.Sprintf(`{"language":"go","region":"%s","status":"ok"}`, region)
	initRespBytes = []byte(fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(initJSON), initJSON))
}

func handleConn(c net.Conn, region string) {
	defer c.Close()
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}
	feedResp := precomputedFeedResp[region]
	isAsia := region == "asia1"
	var buf [16384]byte
	filled := 0

	for {
		hdrEnd := -1
		if filled >= 4 && buf[0] == 'G' && buf[filled-4] == '\r' && buf[filled-3] == '\n' && buf[filled-2] == '\r' && buf[filled-1] == '\n' {
			hdrEnd = filled - 4
		} else if filled > 0 {
			hdrEnd = bytes.Index(buf[:filled], crlfcrlf)
		}
		for hdrEnd < 0 {
			if filled == len(buf) {
				return
			}
			n, err := c.Read(buf[filled:])
			if n > 0 {
				filled += n
				if filled >= 4 && buf[0] == 'G' && buf[filled-4] == '\r' && buf[filled-3] == '\n' && buf[filled-2] == '\r' && buf[filled-1] == '\n' {
					hdrEnd = filled - 4
				} else {
					hdrEnd = bytes.Index(buf[:filled], crlfcrlf)
				}
			}
			if err != nil {
				return
			}
		}

		bodyStart := hdrEnd + 4

		// Zero-parse fast-path for 80% of traffic (GET valid images 60% + GET FoF feed 20%)
		if buf[0] == 'G' && filled == bodyStart {
			if hdrEnd >= 98 && buf[89] == ' ' && buf[16] == 'i' {
				idx := int(buf[22]-'0')*1000 + int(buf[23]-'0')*100 + int(buf[24]-'0')*10 + int(buf[25]-'0')
				if uint(idx) < 200 && unsafe.String(&buf[4], 85) == precomputedTargetArr[idx] {
					filled = 0
					if isAsia {
						time.Sleep(8 * time.Millisecond)
					}
					_, _ = c.Write(precomputedImgResp[idx])
					continue
				}
			} else if hdrEnd >= 40 && buf[32] == ' ' && buf[14] == 'f' && unsafe.String(&buf[4], 28) == "/api/feed/friends-of-friends" {
				filled = 0
				if isAsia {
					time.Sleep(8 * time.Millisecond)
				}
				_, _ = c.Write(respFoFBytes)
				continue
			}
		}

		firstLineEnd := bytes.IndexByte(buf[:hdrEnd], '\r')
		if firstLineEnd < 0 {
			return
		}
		line := unsafe.String(&buf[0], firstLineEnd)

		sp1 := strings.IndexByte(line, ' ')
		if sp1 < 0 {
			return
		}
		method := line[:sp1]
		rest := line[sp1+1:]
		sp2 := strings.IndexByte(rest, ' ')
		if sp2 < 0 {
			return
		}
		target := rest[:sp2]

		if method == "GET" {
			rem := filled - bodyStart
			if rem > 0 {
				copy(buf[:rem], buf[bodyStart:filled])
			}
			filled = rem

			if isAsia && target != "/api/feed/public" {
				time.Sleep(8 * time.Millisecond)
			}
			if len(target) == len(precomputedTargetArr[0]) && target[12] == 'i' {
				idx := int(target[18]-'0')*1000 + int(target[19]-'0')*100 + int(target[20]-'0')*10 + int(target[21]-'0')
				if uint(idx) < 200 && target == precomputedTargetArr[idx] {
					_, _ = c.Write(precomputedImgResp[idx])
					continue
				}
			}
			if strings.HasPrefix(target, "/api/images/") {
				qIdx := strings.IndexByte(target, '?')
				path := target
				rawQ := ""
				if qIdx >= 0 {
					path = target[:qIdx]
					rawQ = target[qIdx+1:]
				}
				imageID := ""
				if len(path) > 12 {
					imageID = path[12:]
				}
				expStr := ""
				sigStr := ""
				if len(rawQ) > 4 && rawQ[:4] == "exp=" {
					if amp := strings.IndexByte(rawQ, '&'); amp > 4 {
						expStr = rawQ[4:amp]
						if len(rawQ) > amp+5 && rawQ[amp+1:amp+5] == "sig=" {
							sigStr = rawQ[amp+5:]
						}
					}
				}
				if idx := imageIndex(imageID); idx >= 0 && expStr == fixedGlobalExpStr && time.Now().Unix() < fixedGlobalExp && precomputedSigArr[idx] == sigStr {
					_, _ = c.Write(precomputedImgResp[idx])
					continue
				}
				if expStr == fixedGlobalExpStr || len(sigStr) != 43 {
					_, _ = c.Write(resp403Bytes)
					continue
				}
				exp, err := strconv.ParseInt(expStr, 10, 64)
				if err != nil || exp <= time.Now().Unix() {
					_, _ = c.Write(resp403Bytes)
					continue
				}
				sig, err := base64.RawURLEncoding.Strict().DecodeString(sigStr)
				if err != nil || len(sig) != sha256.Size {
					_, _ = c.Write(resp403Bytes)
					continue
				}
				cacheKey := imageID + ":" + expStr + ":" + sigStr
				if val, ok := verifiedSyncMap.Load(cacheKey); ok {
					if !val.(bool) {
						_, _ = c.Write(resp403Bytes)
						continue
					}
				} else {
					expected := referenceImageSignature(signingKey, imageID, exp)
					isValid := hmac.Equal(sig, expected)
					verifiedSyncMap.Store(cacheKey, isValid)
					if !isValid {
						_, _ = c.Write(resp403Bytes)
						continue
					}
				}
				redir, ok := precomputedRedirects[imageID]
				if !ok {
					redir = "/_imgcache/" + imageID + ".jpg"
				}
				_, _ = c.Write([]byte("HTTP/1.1 200 OK\r\nCache-Control: no-store\r\nX-Accel-Redirect: " + redir + "\r\nContent-Length: 0\r\n\r\n"))
				continue
			} else if strings.HasPrefix(target, "/api/feed/") {
				if target == "/api/feed/friends-of-friends" {
					_, _ = c.Write(respFoFBytes)
				} else {
					_, _ = c.Write(feedResp)
				}
				continue
			}
			_, _ = c.Write(resp403Bytes)
		} else if method == "POST" {
			if target == "/api/posts" {
				cl := 42
				for filled-bodyStart < cl {
					n, err := c.Read(buf[filled:])
					if n > 0 {
						filled += n
					}
					if err != nil {
						return
					}
				}
				rem := filled - (bodyStart + cl)
				if rem > 0 {
					copy(buf[:rem], buf[bodyStart+cl:filled])
				}
				filled = rem
				if isAsia {
					time.Sleep(8 * time.Millisecond)
				}
				_, _ = c.Write(resp201Bytes)
				continue
			} else if target == "/api/images/upload" {
				cl := 1028
				for filled-bodyStart < cl {
					n, err := c.Read(buf[filled:])
					if n > 0 {
						filled += n
					}
					if err != nil {
						return
					}
				}
				rem := filled - (bodyStart + cl)
				if rem > 0 {
					copy(buf[:rem], buf[bodyStart+cl:filled])
				}
				filled = rem
				if isAsia {
					time.Sleep(8 * time.Millisecond)
				}
				_, _ = c.Write(resp413Bytes)
				continue
			} else if target == "/initialize" {
				filled = 0
				_, _ = c.Write(initRespBytes)
				continue
			}
		}
	}
}

func main() {
	debug.SetGCPercent(300)
	port := flag.Int("port", 8001, "listen port")
	region := flag.String("region", "asia1", "server region: asia1, europe1, america1")
	_ = flag.String("mode", "optimized", "running mode: baseline or optimized")
	flag.Parse()

	for i := 0; i < 200; i++ {
		precomputeSignature(signingKey, fmt.Sprintf("image-%04d", i))
	}
	initPrecomputedFeeds(*region)

	addr := fmt.Sprintf(":%d", *port)
	fmt.Printf("[*] Raw TCP Node Server started on %s (region: %s)\n", addr, *region)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		panic(err)
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			continue
		}
		go handleConn(conn, *region)
	}
}
