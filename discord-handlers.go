package main

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

// DiscordListener holds references to the current BridgeConf
// and BridgeState for use by the event handlers
type DiscordListener struct {
	Bridge *BridgeState
}

func (l *DiscordListener) ready(s *discordgo.Session, event *discordgo.Ready) {
	log.Println("READY event registered")

	//Setup initial discord state
	var g *discordgo.Guild
	g = nil

	for _, i := range event.Guilds {
		if i.ID == l.Bridge.BridgeConfig.GID {
			g = i
		}
	}

	if g == nil {
		log.Println("bad guild on READY")
		return
	}

	for _, vs := range g.VoiceStates {
		if vs.ChannelID == l.Bridge.BridgeConfig.CID {

			u, err := s.User(vs.UserID)
			if err != nil {
				log.Println("error looking up username")
			}

			dm, err := s.UserChannelCreate(u.ID)
			if err != nil {
				log.Println("Error creating private channel for", u.Username)
			}

			l.Bridge.DiscordUsersMutex.Lock()
			l.Bridge.DiscordUsers[vs.UserID] = discordUser{
				username: u.Username,
				seen:     true,
				dm:       dm,
			}
			l.Bridge.DiscordUsersMutex.Unlock()

			// If connected to mumble inform users of Discord users
			if l.Bridge.Connected && !l.Bridge.BridgeConfig.MumbleDisableText {
				l.Bridge.MumbleClient.Do(func() {
					l.Bridge.MumbleClient.Self.Channel.Send(fmt.Sprintf("%v has joined Discord\n", u.Username), false)
				})
			}

		}
	}
}

func (l *DiscordListener) messageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {

	if l.Bridge.Mode == bridgeModeConstant {
		return
	}

	// Ignore all messages created by the bot itself
	if m.Author.ID == s.State.User.ID {
		return
	}
	// Find the channel that the message came from.
	c, err := s.State.Channel(m.ChannelID)
	if err != nil {
		// Could not find channel.
		return
	}

	// Find the guild for that channel.
	g, err := s.State.Guild(c.GuildID)
	if err != nil {
		// Could not find guild.
		return
	}
	prefix := "!" + l.Bridge.BridgeConfig.Command
	if strings.HasPrefix(m.Content, prefix+" link") {
		// Look for the message sender in that guild's current voice states.
		for _, vs := range g.VoiceStates {
			if vs.UserID == m.Author.ID {
				log.Printf("Trying to join GID %v and VID %v\n", g.ID, vs.ChannelID)
				go l.Bridge.startBridge()
				return
			}
		}
	}

	if strings.HasPrefix(m.Content, prefix+" unlink") {
		// Look for the message sender in that guild's current voice states.
		for _, vs := range g.VoiceStates {
			if vs.UserID == m.Author.ID {
				log.Printf("Trying to leave GID %v and VID %v\n", g.ID, vs.ChannelID)
				l.Bridge.BridgeDie <- true
				l.Bridge.BridgeDie = nil
				return
			}
		}
	}

	if strings.HasPrefix(m.Content, prefix+" refresh") {
		// Look for the message sender in that guild's current voice states.
		for _, vs := range g.VoiceStates {
			if vs.UserID == m.Author.ID {
				log.Printf("Trying to refresh GID %v and VID %v\n", g.ID, vs.ChannelID)
				l.Bridge.BridgeDie <- true

				time.Sleep(5 * time.Second)

				go l.Bridge.startBridge()
				return
			}
		}
	}

	if strings.HasPrefix(m.Content, prefix+" auto") {
		if l.Bridge.Mode != bridgeModeAuto {
			l.Bridge.Mode = bridgeModeAuto
			l.Bridge.AutoChanDie = make(chan bool)
			go l.Bridge.AutoBridge()
		} else {
			l.Bridge.AutoChanDie <- true
			l.Bridge.Mode = bridgeModeManual
		}
	}
}

func (l *DiscordListener) guildCreate(s *discordgo.Session, event *discordgo.GuildCreate) {

	if event.Guild.Unavailable {
		return
	}

	for _, channel := range event.Guild.Channels {
		if channel.ID == event.Guild.ID {
			log.Println("Mumble-Discord bridge is active in new guild")
			return
		}
	}
}

func (l *DiscordListener) voiceUpdate(s *discordgo.Session, event *discordgo.VoiceStateUpdate) {
	l.Bridge.DiscordUsersMutex.Lock()
	defer l.Bridge.DiscordUsersMutex.Unlock()

	if event.GuildID == l.Bridge.BridgeConfig.GID {

		g, err := s.State.Guild(l.Bridge.BridgeConfig.GID)
		if err != nil {
			log.Println("error finding guild")
			panic(err)
		}

		for u := range l.Bridge.DiscordUsers {
			du := l.Bridge.DiscordUsers[u]
			du.seen = false
			l.Bridge.DiscordUsers[u] = du
		}

		// Sync the channel voice states to the local discordUsersMap
		for _, vs := range g.VoiceStates {
			if vs.ChannelID == l.Bridge.BridgeConfig.CID {
				if s.State.User.ID == vs.UserID {
					// Ignore bot
					continue
				}

				if _, ok := l.Bridge.DiscordUsers[vs.UserID]; !ok {

					u, err := s.User(vs.UserID)
					if err != nil {
						log.Println("error looking up username")
						continue
					}

					println("User joined Discord " + u.Username)
					dm, err := s.UserChannelCreate(u.ID)
					if err != nil {
						log.Println("Error creating private channel for", u.Username)
					}
					l.Bridge.DiscordUsers[vs.UserID] = discordUser{
						username: u.Username,
						seen:     true,
						dm:       dm,
					}
					if l.Bridge.Connected && !l.Bridge.BridgeConfig.MumbleDisableText {
						l.Bridge.MumbleClient.Do(func() {
							l.Bridge.MumbleClient.Self.Channel.Send(fmt.Sprintf("%v has joined Discord\n", u.Username), false)
						})
					}
				} else {
					du := l.Bridge.DiscordUsers[vs.UserID]
					du.seen = true
					l.Bridge.DiscordUsers[vs.UserID] = du
				}

			}
		}

		// Remove users that are no longer connected
		for id := range l.Bridge.DiscordUsers {
			if l.Bridge.DiscordUsers[id].seen == false {
				println("User left Discord channel " + l.Bridge.DiscordUsers[id].username)
				if l.Bridge.Connected && !l.Bridge.BridgeConfig.MumbleDisableText {
					l.Bridge.MumbleClient.Do(func() {
						l.Bridge.MumbleClient.Self.Channel.Send(fmt.Sprintf("%v has left Discord channel\n", l.Bridge.DiscordUsers[id].username), false)
					})
				}
				delete(l.Bridge.DiscordUsers, id)
			}
		}
	}
}
