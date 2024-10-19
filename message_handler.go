package main

import (
	"context"
	"fmt"
	"log"
	"reflect"
	"sync"
	"time"

	"github.com/bluenviron/gomavlib/v3"
	"github.com/bluenviron/gomavlib/v3/pkg/dialects/common"
	"github.com/bluenviron/gomavlib/v3/pkg/message"
	"github.com/bluenviron/gomavlib/v3/pkg/frame"

)

const (
	nodeInactiveAfter = 30 * time.Second
)

var zero reflect.Value

//VK
var comTimeLast time.Time
//VK

func getTarget(msg message.Message) (byte, byte, bool) {
	rv := reflect.ValueOf(msg).Elem()
	ts := rv.FieldByName("TargetSystem")
	tc := rv.FieldByName("TargetComponent")

	if ts != zero && tc != zero {
		return byte(ts.Uint()), byte(tc.Uint()), true
	}

	return 0, 0, false
}

type remoteNodeKey struct {
	channel     *gomavlib.Channel
	systemID    byte
	componentID byte
}

func (i remoteNodeKey) String() string {
	return fmt.Sprintf("chan=%s sid=%d cid=%d", i.channel, i.systemID, i.componentID)
}

type messageHandler struct {
	ctx              context.Context
	wg               *sync.WaitGroup
	streamreqDisable bool
	node             *gomavlib.Node

	remoteNodeMutex sync.Mutex
	remoteNodes     map[remoteNodeKey]time.Time
	disableGCS      bool
	activeGCS       byte
}

func newMessageHandler(
	ctx context.Context,
	wg *sync.WaitGroup,
	streamreqDisable bool,
	node *gomavlib.Node,
) (*messageHandler, error) {
	mh := &messageHandler{
		ctx:              ctx,
		wg:               wg,
		streamreqDisable: streamreqDisable,
		node:             node,
		remoteNodes:      make(map[remoteNodeKey]time.Time),
		disableGCS:       false,
		activeGCS:        0,
	}

	wg.Add(1)
	go mh.run()

	return mh, nil
}

func (mh *messageHandler) run() {
	defer mh.wg.Done()

	// delete remote nodes after a period of inactivity
	for {
		select {
		case <-time.After(10 * time.Second):
			func() {
				now := time.Now()

				mh.remoteNodeMutex.Lock()
				defer mh.remoteNodeMutex.Unlock()

				for rnode, t := range mh.remoteNodes {
					if now.Sub(t) >= nodeInactiveAfter {
						log.Printf("node disappeared: %s", rnode)
						delete(mh.remoteNodes, rnode)
					}
				}
			}()
//VK
		case <-time.After(1 * time.Second):
			func() {
				now := time.Now()

					if ( mh.disableGCS ==true ) &&(now.Sub(comTimeLast) >= 2*time.Second ) {
						log.Printf("TIMEOUT, return from tracking mode to normal, %s", comTimeLast)
						mh.disableGCS = false
					}
			}()


//VK
		case <-mh.ctx.Done():
			return
		}
	}
}

func (mh *messageHandler) findNodeBySystemID(systemID byte) *remoteNodeKey {
	for key := range mh.remoteNodes {
		if key.systemID == systemID {
			return &key
		}
	}
	return nil
}

func (mh *messageHandler) findNodeBySystemAndComponentID(systemID byte, componentID byte) *remoteNodeKey {
	for key := range mh.remoteNodes {
		if key.systemID == systemID && key.componentID == componentID {
			return &key
		}
	}
	return nil
}

func (mh *messageHandler) onEventFrame(evt *gomavlib.EventFrame) {
	key := remoteNodeKey{
		channel:     evt.Channel,
		systemID:    evt.SystemID(),
		componentID: evt.ComponentID(),
	}

	func() {
		mh.remoteNodeMutex.Lock()
		defer mh.remoteNodeMutex.Unlock()

		if _, ok := mh.remoteNodes[key]; !ok {
			log.Printf("node appeared: %s", key)
		}

		mh.remoteNodes[key] = time.Now()
	}()

	// stop stream request messages
	if !mh.streamreqDisable {
		if _, ok := evt.Message().(*common.MessageRequestDataStream); ok {
			return
		}
	}

	currentFrame := evt.Frame
	//currentMessage := nil

	switch msg := evt.Message().(type) {
	// if frm.Message() is a *ardupilotmega.MessageHeartbeat, access its fields
	//case *ardupilotmega.MessageHeartbeat:
	//    log.Printf("received heartbeat (type %d)\n", msg.Type)

	    // if frm.Message() is a *ardupilotmega.MessageServoOutputRaw, access its fields
	//case *ardupilotmega.MessageServoOutputRaw:
	//    log.Printf("received servo output with values: %d %d %d %d %d %d %d %d\n",
	//    msg.Servo1Raw, msg.Servo2Raw, msg.Servo3Raw, msg.Servo4Raw,
	//    msg.Servo5Raw, msg.Servo6Raw, msg.Servo7Raw, msg.Servo8Raw)
	case *common.MessageCommandLong:
	    log.Printf("CommandLong, cmd:%d, param1: %f, systemId: %d\n", msg.Command, msg.Param1, evt.SystemID())
	    if msg.Command == 31014 {
		    if msg.Param1 != 0 {
			mh.disableGCS = true
			mh.activeGCS = evt.SystemID()
//VK
			comTimeLast = time.Now()
//VK
		    } else {
			mh.disableGCS = false
		    }
	    }
	case *common.MessageRcChannelsOverride:
	    log.Printf("RC: system:%d, component:%d\n", evt.Frame.GetSystemID(), evt.Frame.GetComponentID())
	    if mh.disableGCS == true {
		log.Printf("Disabled RC_OVERRIDE\n")
		if  evt.SystemID() != mh.activeGCS {
			msg.Chan1Raw = 0xFFFF
			msg.Chan2Raw = 0xFFFF
			msg.Chan3Raw = 0xFFFF
			msg.Chan4Raw = 0xFFFF
			currentFrame = &frame.V2Frame{
				SequenceNumber: evt.Frame.GetSequenceNumber(),
				SystemID: evt.Frame.GetSystemID(),
				ComponentID: evt.Frame.GetComponentID(),
				Message: msg,
				}
		} else {
			currentFrame = &frame.V2Frame{
				SequenceNumber: evt.Frame.GetSequenceNumber(),
				SystemID: 255,
				ComponentID: evt.Frame.GetComponentID(),
				Message: msg,
				}
		}
		mh.node.FixFrame(currentFrame)

	    }
	}


	// if message has a target, route only to it
	systemID, componentID, hasTarget := getTarget(evt.Message())
	if hasTarget && systemID > 0 {
		var key *remoteNodeKey
		if componentID == 0 {
			key = mh.findNodeBySystemID(systemID)
		} else {
			key = mh.findNodeBySystemAndComponentID(systemID, componentID)
		}

		if key != nil {
			if key.channel == evt.Channel {
				log.Printf("Warning: channel %s attempted to send message to itself, discarding", key.channel)
			} else {
				mh.node.WriteFrameTo(key.channel, currentFrame) //nolint:errcheck
				return
			}
		} else {
			log.Printf(
				"Warning: received message addressed to unexistent node with systemID=%d and componentID=%d",
				systemID, componentID)
		}
	}

	// otherwise, route message to every channel
	mh.node.WriteFrameExcept(evt.Channel, currentFrame) //nolint:errcheck
}

func (mh *messageHandler) onEventChannelClose(evt *gomavlib.EventChannelClose) {
	mh.remoteNodeMutex.Lock()
	defer mh.remoteNodeMutex.Unlock()

	// delete remote nodes associated to channel
	for key := range mh.remoteNodes {
		if key.channel == evt.Channel {
			delete(mh.remoteNodes, key)
			log.Printf("node disappeared: %s", key)
		}
	}
}
