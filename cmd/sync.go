/*
 * JuiceFS, Copyright (C) 2018 Juicedata, Inc.
 *
 * This program is free software: you can use, redistribute, and/or modify
 * it under the terms of the GNU Affero General Public License, version 3
 * or later ("AGPL"), as published by the Free Software Foundation.
 *
 * This program is distributed in the hope that it will be useful, but WITHOUT
 * ANY WARRANTY; without even the implied warranty of MERCHANTABILITY or
 * FITNESS FOR A PARTICULAR PURPOSE.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program. If not, see <http://www.gnu.org/licenses/>.
 */

package main

import (
	"fmt"
	"net"
	"net/http"
	_ "net/http/pprof"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/juicedata/juicefs/pkg/object"
	"github.com/juicedata/juicefs/pkg/sync"
	"github.com/juicedata/juicefs/pkg/utils"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
	"golang.org/x/term"
)

func supportHTTPS(name, endpoint string) bool {
	switch name {
	case "ufile":
		return !(strings.Contains(endpoint, ".internal-") || strings.HasSuffix(endpoint, ".ucloud.cn"))
	case "oss":
		return !(strings.Contains(endpoint, ".vpc100-oss") || strings.Contains(endpoint, "internal.aliyuncs.com"))
	case "jss":
		return false
	case "s3":
		ps := strings.SplitN(strings.Split(endpoint, ":")[0], ".", 2)
		if len(ps) > 1 && net.ParseIP(ps[1]) != nil {
			return false
		}
	case "minio":
		return false
	}
	return true
}

func createSyncStorage(uri string, conf *sync.Config) (object.ObjectStorage, error) {
	if !strings.Contains(uri, "://") {
		if strings.Contains(uri, ":") {
			var user string
			if strings.Contains(uri, "@") {
				parts := strings.Split(uri, "@")
				user = parts[0]
				uri = parts[1]
			}
			var pass string
			if strings.Contains(user, ":") {
				parts := strings.Split(user, ":")
				user = parts[0]
				pass = parts[1]
			} else if os.Getenv("SSH_PRIVATE_KEY_PATH") == "" {
				fmt.Print("Enter Password: ")
				bytePassword, err := term.ReadPassword(int(syscall.Stdin))
				if err != nil {
					logger.Fatalf("Read password: %s", err.Error())
				}
				pass = string(bytePassword)
			}
			return object.CreateStorage("sftp", uri, user, pass)
		}
		fullpath, err := filepath.Abs(uri)
		if err != nil {
			logger.Fatalf("invalid path: %s", err.Error())
		}
		if strings.HasSuffix(uri, "/") {
			fullpath += "/"
		}
		uri = "file://" + fullpath
	}
	u, err := url.Parse(uri)
	if err != nil {
		logger.Fatalf("Can't parse %s: %s", uri, err.Error())
	}
	user := u.User
	var accessKey, secretKey string
	if user != nil {
		accessKey = user.Username()
		secretKey, _ = user.Password()
	}
	name := strings.ToLower(u.Scheme)
	endpoint := u.Host
	if name == "file" {
		endpoint = u.Path
	} else if name == "hdfs" {
	} else if !conf.NoHTTPS && supportHTTPS(name, endpoint) {
		endpoint = "https://" + endpoint
	} else {
		endpoint = "http://" + endpoint
	}

	store, err := object.CreateStorage(name, endpoint, accessKey, secretKey)
	if err != nil {
		return nil, fmt.Errorf("create %s %s: %s", name, endpoint, err)
	}
	if conf.Perms {
		if _, ok := store.(object.FileSystem); !ok {
			logger.Warnf("%s is not a file system, can not preserve permissions", store)
			conf.Perms = false
		}
	}
	if name != "file" && len(u.Path) > 1 {
		store = object.WithPrefix(store, u.Path[1:])
	}
	return store, nil
}

const USAGE = `juicefs [options] sync [options] SRC DST
SRC and DST should be [NAME://][ACCESS_KEY:SECRET_KEY@]BUCKET[.ENDPOINT][/PREFIX]`

func doSync(c *cli.Context) error {
	args := extractArgs(c)
	if len(args) != 2 {
		logger.Errorf(USAGE)
		return nil
	}
	config := sync.NewConfigFromCli(c)
	go func() { _ = http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", config.HTTPPort), nil) }()

	if config.Verbose {
		utils.SetLogLevel(logrus.DebugLevel)
	} else if config.Quiet {
		utils.SetLogLevel(logrus.ErrorLevel)
	}
	utils.InitLoggers(false)

	if strings.HasSuffix(args[0], "/") != strings.HasSuffix(args[1], "/") {
		logger.Fatalf("SRC and DST should both end with '/' or not!")
	}
	src, err := createSyncStorage(args[0], config)
	if err != nil {
		return err
	}
	dst, err := createSyncStorage(args[1], config)
	if err != nil {
		return err
	}
	return sync.Sync(src, dst, config)
}

func syncFlags() *cli.Command {
	return &cli.Command{
		Name:      "sync",
		Usage:     "sync between two storage",
		ArgsUsage: "SRC DST",
		Action:    doSync,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "start",
				Aliases: []string{"s"},
				Value:   "",
				Usage:   "the first `KEY` to sync",
			},
			&cli.StringFlag{
				Name:    "end",
				Aliases: []string{"e"},
				Value:   "",
				Usage:   "the last `KEY` to sync",
			},
			&cli.IntFlag{
				Name:    "threads",
				Aliases: []string{"p"},
				Value:   10,
				Usage:   "number of concurrent threads",
			},
			&cli.IntFlag{
				Name:  "http-port",
				Value: 6070,
				Usage: "HTTP `PORT` to listen to",
			},
			&cli.BoolFlag{
				Name:    "update",
				Aliases: []string{"u"},
				Usage:   "update existing file if the source is newer",
			},
			&cli.BoolFlag{
				Name:    "force-update",
				Aliases: []string{"f"},
				Usage:   "always update existing file",
			},
			&cli.BoolFlag{
				Name:  "perms",
				Usage: "preserve permissions",
			},
			&cli.BoolFlag{
				Name:  "dirs",
				Usage: "Sync directories or holders",
			},
			&cli.BoolFlag{
				Name:  "dry",
				Usage: "don't copy file",
			},
			&cli.BoolFlag{
				Name:    "delete-src",
				Aliases: []string{"deleteSrc"},
				Usage:   "delete objects from source after synced",
			},
			&cli.BoolFlag{
				Name:    "delete-dst",
				Aliases: []string{"deleteDst"},
				Usage:   "delete extraneous objects from destination",
			},
			&cli.StringSliceFlag{
				Name:  "exclude",
				Usage: "exclude keys containing `PATTERN` (POSIX regular expressions)",
			},
			&cli.StringSliceFlag{
				Name:  "include",
				Usage: "only include keys containing `PATTERN` (POSIX regular expressions)",
			},
			&cli.StringFlag{
				Name:  "manager",
				Usage: "manager address",
			},
			&cli.StringSliceFlag{
				Name:  "worker",
				Usage: "hosts (seperated by comma) to launch worker",
			},
			&cli.IntFlag{
				Name:  "bwlimit",
				Usage: "limit bandwidth in Mbps (0 means unlimited)",
			},
			&cli.BoolFlag{
				Name:  "no-https",
				Usage: "donot use HTTPS",
			},
		},
	}
}
