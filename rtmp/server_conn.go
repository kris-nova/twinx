// Copyright © 2021 Kris Nóva <kris@nivenly.com>
// Copyright (c) 2017 吴浩麟
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// ────────────────────────────────────────────────────────────────────────────
//
//  ████████╗██╗    ██╗██╗███╗   ██╗██╗  ██╗
//  ╚══██╔══╝██║    ██║██║████╗  ██║╚██╗██╔╝
//     ██║   ██║ █╗ ██║██║██╔██╗ ██║ ╚███╔╝
//     ██║   ██║███╗██║██║██║╚██╗██║ ██╔██╗
//     ██║   ╚███╔███╔╝██║██║ ╚████║██╔╝ ██╗
//     ╚═╝    ╚══╝╚══╝ ╚═╝╚═╝  ╚═══╝╚═╝  ╚═╝
//
// ────────────────────────────────────────────────────────────────────────────

package rtmp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/gwuhaolin/livego/protocol/amf"
	"github.com/kris-nova/logger"
)

type ServerConn struct {

	// conn is the base conn for all RTMP members (both clients, and servers)
	conn *Conn

	// transactionID is passed around
	// with the packets to/from a compliant RTMP member
	//
	// This can be reset to 0, and should increment at times
	transactionID int64

	// connectInfo is a well-known RTMP object that is passed
	// around with compliant RTMP members
	connectInfo *ConnectInfo

	// connectPacket is the single *ChunkStream packet
	// discovered when a client calls connect
	connectPacket *ChunkStream

	// publishInfo will be set if this connection is an RTMP publish
	// client
	publishInfo *PublishInfo

	// Every stream conn has an RTMP url
	// Every stream conn has a stream
	stream *SafeBoundedBuffer

	decoder *amf.Decoder
	encoder *amf.Encoder
	bytesw  *bytes.Buffer
}

func NewServerConn(conn *Conn) *ServerConn {
	return &ServerConn{
		conn:    conn,
		bytesw:  bytes.NewBuffer(nil),
		decoder: &amf.Decoder{},
		encoder: &amf.Encoder{},
	}
}

// NextChunk will read the next packet of data from the client,
// and will attempt to respond to the packet based on it's content and
// the appropriate response per the RTMP spec.
func (s *ServerConn) NextChunk() (*ChunkStream, error) {
	var chunk ChunkStream
	if err := s.conn.Read(&chunk); err != nil {
		return nil, fmt.Errorf("reading chunk from client: %v", err)
	}
	return &chunk, nil
}

func (s *ServerConn) RoutePackets() error {
	for {
		x, err := s.NextChunk()
		if err != nil {
			// Terminate the client!
			if err != TestableEOFError {
				return err
			}
		}
		err = s.Route(x)
		if err != nil {
			logger.Critical(err.Error())
		}
	}
	return nil
}

func (s *ServerConn) Route(x *ChunkStream) error {
	switch x.TypeID {
	case SetChunkSizeMessageID:
		logger.Debug(rtmpMessage(typeIDString(x), rx))
		chunkSize := binary.BigEndian.Uint32(x.Data)
		s.conn.remoteChunkSize = chunkSize
		logger.Debug(rtmpMessage(typeIDString(x), ack))
		s.conn.ack(x.Length)
	case AbortMessageID:
		logger.Critical("unsupported messageID: %s", typeIDString(x))
	case AcknowledgementMessageID:
		logger.Critical("server unsupported messageID: %s", typeIDString(x))
	case WindowAcknowledgementSizeMessageID:
		logger.Debug(rtmpMessage(typeIDString(x), rx))
		ackSize := binary.BigEndian.Uint32(x.Data)
		s.conn.remoteWindowAckSize = ackSize
		logger.Debug(rtmpMessage(typeIDString(x), ack))
		s.conn.ack(x.Length)
	case SetPeerBandwidthMessageID:
		logger.Critical("unsupported messageID: %s", typeIDString(x))
	case UserControlMessageID:
		logger.Critical("unsupported messageID: %s", typeIDString(x))
	case CommandMessageAMF0ID, CommandMessageAMF3ID:
		//logger.Debug(rtmpMessage(typeIDString(x), rx))
		// Handle the command message
		// Note: There are sub-command messages logged in the next method
		err := s.handleCommand(x)
		if err != nil {
			logger.Critical("command message: %v", err)
		}
	case DataMessageAMF0ID, DataMessageAMF3ID:
		logger.Debug(rtmpMessage(typeIDString(x), rx))
		s.stream.Write(x)
	case SharedObjectMessageAMF0ID, SharedObjectMessageAMF3ID:
		logger.Critical("unsupported messageID: %s", typeIDString(x))
	case AudioMessageID:
		//logger.Debug(rtmpMessage(typeIDString(x), rx))
		if s.publishInfo != nil {
			s.stream.Write(x)
		}
	case VideoMessageID:
		//logger.Debug(rtmpMessage(typeIDString(x), rx))
		if s.publishInfo != nil {
			s.stream.Write(x)
		}
	case AggregateMessageID:
		logger.Critical("unsupported messageID: %s", typeIDString(x))
	default:
		logger.Critical("unsupported messageID: %s", typeIDString(x))

	}
	return nil
}

// routeCommand is a sub-router for any of the command messages.
// the server router will receive a set of requests from the client
// the commands can be unordered and of different type
//
// this is the main router for all of these commands that start out
// as an unknown interface
func (s *ServerConn) routeCommand(commandName string, x *ChunkStream) error {
	switch commandName {
	case CommandConnect:
		//logger.Debug(rtmpMessage(fmt.Sprintf("command.%s", CommandConnect), rx))
		return s.connectRX(x)
	case CommandCreateStream:
		//logger.Debug(rtmpMessage(fmt.Sprintf("command.%s", CommandCreateStream), rx))
		return s.createStreamRX(x)
	case CommandPublish:
		//logger.Debug(rtmpMessage(fmt.Sprintf("command.%s", CommandPublish), rx))
		err := s.publishRX(x)
		if err != nil {
			return err
		}
		// Write packets to the stream
		logger.Info(rtmpMessage("Publish Stream", stream))
		s.stream.Stream()
	case CommandPlay:
		//logger.Debug(rtmpMessage(fmt.Sprintf("command.%s", CommandPlay), rx))
		err := s.playRX(x)
		if err != nil {
			return err
		}
		// Read packets from the stream
		playWriter := NewPlayWriter(s)
		s.stream.AddWriter(playWriter)
		logger.Info(rtmpMessage("Play Stream", stream))

	default:
		return fmt.Errorf("unsupported commandName: %s", commandName)
	}
	return nil
}

func (s *ServerConn) handleCommand(x *ChunkStream) error {
	amfType := amf.AMF0
	if x.TypeID == CommandMessageAMF3ID {
		// Arithmetic to match AMF3 encoding
		amfType = amf.AMF3
		x.Data = x.Data[1:]
	}
	r := bytes.NewReader(x.Data)

	// enable logging here (or in the logger...)
	//vs, err := s.decoder.DecodeBatch(r, amf.Version(amfType))
	vs, err := s.LogDecodeBatch(r, amf.Version(amfType))
	if err != nil && err != io.EOF {
		return err
	}

	// set batchedValues
	x.batchedValues = vs

	// We assume the first message is the name, and in array location 0
	// Validate this before anything else.
	if len(vs) <= 1 {
		return errors.New("decoder failure: unable to decode from protocol")
	}
	commandName, ok := vs[0].(string)
	if !ok {
		return errors.New("decoder failure: unable to render command name")
	}
	return s.routeCommand(commandName, x)
}

const (
	CommandConnectWellKnownID float64 = 1
)

func (s *ServerConn) Write(packet ChunkStream) error {
	if packet.TypeID == TAG_SCRIPTDATAAMF0 ||
		packet.TypeID == TAG_SCRIPTDATAAMF3 {
		var err error
		if packet.Data, err = amf.MetaDataReform(packet.Data, amf.DEL); err != nil {
			return err
		}
		packet.Length = uint32(len(packet.Data))
	}
	return s.conn.Write(&packet)
}

func (s *ServerConn) Flush() error {
	return s.conn.Flush()
}

func (s *ServerConn) Read(packet *ChunkStream) (err error) {
	return s.conn.Read(packet)
}

func (s *ServerConn) LogDecodeBatch(r io.Reader, ver amf.Version) ([]interface{}, error) {

	vs, err := s.decoder.DecodeBatch(r, ver)
	for k, v := range vs {
		logger.Debug("  [%+v] (%+v)", k, v)
	}
	return vs, err
}

func (s *ServerConn) Close() {
	s.conn.Close()
}

func (s *ServerConn) writeMsg(csid, streamID uint32, args ...interface{}) error {
	s.bytesw.Reset()
	for _, v := range args {
		if _, err := s.encoder.Encode(s.bytesw, v, amf.AMF0); err != nil {
			return err
		}
	}
	msg := s.bytesw.Bytes()
	packet := ChunkStream{
		Format:    0,
		CSID:      csid,
		Timestamp: 0,
		TypeID:    20,
		StreamID:  streamID,
		Length:    uint32(len(msg)),
		Data:      msg,
	}
	s.conn.Write(&packet)
	return s.conn.Flush()
}
