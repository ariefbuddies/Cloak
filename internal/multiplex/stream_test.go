package multiplex

import (
	"bytes"
	"github.com/cbeuw/Cloak/internal/common"
	"io"
	"math/rand"
	"net"
	"testing"
	"time"

	"github.com/cbeuw/connutil"
)

const payloadLen = 1000

func setupSesh(unordered bool, key [32]byte) *Session {
	obfuscator, _ := MakeObfuscator(0x00, key)

	seshConfig := SessionConfig{
		Obfuscator: obfuscator,
		Valve:      nil,
		Unordered:  unordered,
	}
	return MakeSession(0, seshConfig)
}

func BenchmarkStream_Write_Ordered(b *testing.B) {
	hole := connutil.Discard()
	var sessionKey [32]byte
	rand.Read(sessionKey[:])
	sesh := setupSesh(false, sessionKey)
	sesh.AddConnection(hole)
	testData := make([]byte, payloadLen)
	rand.Read(testData)

	stream, _ := sesh.OpenStream()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := stream.Write(testData)
		if err != nil {
			b.Error(
				"For", "stream write",
				"got", err,
			)
		}
		b.SetBytes(payloadLen)
	}
}

func BenchmarkStream_Read_Ordered(b *testing.B) {
	var sessionKey [32]byte
	rand.Read(sessionKey[:])
	sesh := setupSesh(false, sessionKey)
	testPayload := make([]byte, payloadLen)
	rand.Read(testPayload)

	f := &Frame{
		1,
		0,
		0,
		testPayload,
	}

	obfsBuf := make([]byte, 17000)

	l, _ := net.Listen("tcp", "127.0.0.1:0")
	go func() {
		// potentially bottlenecked here rather than the actual stream read throughput
		conn, _ := net.Dial("tcp", l.Addr().String())
		for {
			i, _ := sesh.Obfs(f, obfsBuf)
			f.Seq += 1
			_, err := conn.Write(obfsBuf[:i])
			if err != nil {
				b.Error("cannot write to connection", err)
			}
		}
	}()
	conn, _ := l.Accept()

	sesh.AddConnection(conn)
	stream, err := sesh.Accept()
	if err != nil {
		b.Error("failed to accept stream", err)
	}

	//time.Sleep(5*time.Second) // wait for buffer to fill up

	readBuf := make([]byte, payloadLen)
	b.ResetTimer()
	for j := 0; j < b.N; j++ {
		n, err := stream.Read(readBuf)
		if !bytes.Equal(readBuf, testPayload) {
			b.Error("paylod not equal")
		}
		b.SetBytes(int64(n))
		if err != nil {
			b.Error(err)
		}
	}

}

func TestStream_Write(t *testing.T) {
	hole := connutil.Discard()
	var sessionKey [32]byte
	rand.Read(sessionKey[:])
	sesh := setupSesh(false, sessionKey)
	sesh.AddConnection(hole)
	testData := make([]byte, payloadLen)
	rand.Read(testData)

	stream, _ := sesh.OpenStream()
	_, err := stream.Write(testData)
	if err != nil {
		t.Error(
			"For", "stream write",
			"got", err,
		)
	}
}

func TestStream_WriteSync(t *testing.T) {
	// Close calls made after write MUST have a higher seq
	var sessionKey [32]byte
	rand.Read(sessionKey[:])
	clientSesh := setupSesh(false, sessionKey)
	serverSesh := setupSesh(false, sessionKey)
	w, r := connutil.AsyncPipe()
	clientSesh.AddConnection(&common.TLSConn{Conn: w})
	serverSesh.AddConnection(&common.TLSConn{Conn: r})
	testData := make([]byte, payloadLen)
	rand.Read(testData)

	t.Run("test single", func(t *testing.T) {
		go func() {
			stream, _ := clientSesh.OpenStream()
			stream.Write(testData)
			stream.Close()
		}()

		recvBuf := make([]byte, payloadLen)
		serverStream, _ := serverSesh.Accept()
		_, err := io.ReadFull(serverStream, recvBuf)
		if err != nil {
			t.Error(err)
		}
	})

	t.Run("test multiple", func(t *testing.T) {
		const numStreams = 100
		for i := 0; i < numStreams; i++ {
			go func() {
				stream, _ := clientSesh.OpenStream()
				stream.Write(testData)
				stream.Close()
			}()
		}
		for i := 0; i < numStreams; i++ {
			recvBuf := make([]byte, payloadLen)
			serverStream, _ := serverSesh.Accept()
			_, err := io.ReadFull(serverStream, recvBuf)
			if err != nil {
				t.Error(err)
			}
		}
	})
}

func TestStream_Close(t *testing.T) {
	var sessionKey [32]byte
	rand.Read(sessionKey[:])
	sesh := setupSesh(false, sessionKey)
	testPayload := []byte{42, 42, 42}

	f := &Frame{
		1,
		0,
		0,
		testPayload,
	}

	conn, writingEnd := connutil.AsyncPipe()
	sesh.AddConnection(conn)
	obfsBuf := make([]byte, 512)
	i, _ := sesh.Obfs(f, obfsBuf)
	writingEnd.Write(obfsBuf[:i])
	time.Sleep(100 * time.Microsecond)
	stream, err := sesh.Accept()
	if err != nil {
		t.Error("failed to accept stream", err)
		return
	}
	err = stream.Close()
	if err != nil {
		t.Error("failed to actively close stream", err)
		return
	}

	if sI, _ := sesh.streams.Load(stream.(*Stream).id); sI != nil {
		t.Error("stream still exists")
		return
	}

	readBuf := make([]byte, len(testPayload))
	_, err = io.ReadFull(stream, readBuf)
	if err != nil {
		t.Errorf("can't read residual data %v", err)
	}
	if !bytes.Equal(readBuf, testPayload) {
		t.Errorf("read wrong data")
	}
}

func TestStream_Read(t *testing.T) {
	var sessionKey [32]byte
	rand.Read(sessionKey[:])
	sesh := setupSesh(false, sessionKey)
	testPayload := []byte{42, 42, 42}
	const smallPayloadLen = 3

	f := &Frame{
		1,
		0,
		0,
		testPayload,
	}

	conn, writingEnd := connutil.AsyncPipe()
	sesh.AddConnection(conn)

	var streamID uint32
	buf := make([]byte, 10)

	obfsBuf := make([]byte, 512)
	t.Run("Plain read", func(t *testing.T) {
		f.StreamID = streamID
		i, _ := sesh.Obfs(f, obfsBuf)
		streamID++
		writingEnd.Write(obfsBuf[:i])
		time.Sleep(100 * time.Microsecond)
		stream, err := sesh.Accept()
		if err != nil {
			t.Error("failed to accept stream", err)
			return
		}
		i, err = stream.Read(buf)
		if err != nil {
			t.Error("failed to read", err)
			return
		}
		if i != smallPayloadLen {
			t.Errorf("expected read %v, got %v", smallPayloadLen, i)
			return
		}
		if !bytes.Equal(buf[:i], testPayload) {
			t.Error("expected", testPayload,
				"got", buf[:i])
			return
		}
	})
	t.Run("Nil buf", func(t *testing.T) {
		f.StreamID = streamID
		i, _ := sesh.Obfs(f, obfsBuf)
		streamID++
		writingEnd.Write(obfsBuf[:i])
		time.Sleep(100 * time.Microsecond)
		stream, _ := sesh.Accept()
		i, err := stream.Read(nil)
		if i != 0 || err != nil {
			t.Error("expecting", 0, nil,
				"got", i, err)
		}
	})
	t.Run("Read after stream close", func(t *testing.T) {
		f.StreamID = streamID
		i, _ := sesh.Obfs(f, obfsBuf)
		streamID++
		writingEnd.Write(obfsBuf[:i])
		time.Sleep(100 * time.Microsecond)
		stream, _ := sesh.Accept()
		stream.Close()
		i, err := stream.Read(buf)
		if err != nil {
			t.Error("failed to read", err)
		}
		if i != smallPayloadLen {
			t.Errorf("expected read %v, got %v", smallPayloadLen, i)
		}
		if !bytes.Equal(buf[:i], testPayload) {
			t.Error("expected", testPayload,
				"got", buf[:i])
		}
		_, err = stream.Read(buf)
		if err == nil {
			t.Error("expecting error", ErrBrokenStream,
				"got nil error")
		}
	})
	t.Run("Read after session close", func(t *testing.T) {
		f.StreamID = streamID
		i, _ := sesh.Obfs(f, obfsBuf)
		streamID++
		writingEnd.Write(obfsBuf[:i])
		time.Sleep(100 * time.Microsecond)
		stream, _ := sesh.Accept()
		sesh.Close()
		i, err := stream.Read(buf)
		if err != nil {
			t.Error("failed to read", err)
		}
		if i != smallPayloadLen {
			t.Errorf("expected read %v, got %v", smallPayloadLen, i)
		}
		if !bytes.Equal(buf[:i], testPayload) {
			t.Error("expected", testPayload,
				"got", buf[:i])
		}
		_, err = stream.Read(buf)
		if err == nil {
			t.Error("expecting error", ErrBrokenStream,
				"got nil error")
		}
	})

}

func TestStream_UnorderedRead(t *testing.T) {
	var sessionKey [32]byte
	rand.Read(sessionKey[:])
	sesh := setupSesh(false, sessionKey)
	testPayload := []byte{42, 42, 42}
	const smallPayloadLen = 3

	f := &Frame{
		1,
		0,
		0,
		testPayload,
	}

	conn, writingEnd := connutil.AsyncPipe()
	sesh.AddConnection(conn)

	var streamID uint32
	buf := make([]byte, 10)

	obfsBuf := make([]byte, 512)
	t.Run("Plain read", func(t *testing.T) {
		f.StreamID = streamID
		i, _ := sesh.Obfs(f, obfsBuf)
		streamID++
		writingEnd.Write(obfsBuf[:i])
		time.Sleep(100 * time.Microsecond)
		stream, err := sesh.Accept()
		if err != nil {
			t.Error("failed to accept stream", err)
		}
		i, err = stream.Read(buf)
		if err != nil {
			t.Error("failed to read", err)
		}
		if i != smallPayloadLen {
			t.Errorf("expected read %v, got %v", smallPayloadLen, i)
		}
		if !bytes.Equal(buf[:i], testPayload) {
			t.Error("expected", testPayload,
				"got", buf[:i])
		}
	})
	t.Run("Nil buf", func(t *testing.T) {
		f.StreamID = streamID
		i, _ := sesh.Obfs(f, obfsBuf)
		streamID++
		writingEnd.Write(obfsBuf[:i])
		time.Sleep(100 * time.Microsecond)
		stream, _ := sesh.Accept()
		i, err := stream.Read(nil)
		if i != 0 || err != nil {
			t.Error("expecting", 0, nil,
				"got", i, err)
		}
	})
	t.Run("Read after stream close", func(t *testing.T) {
		f.StreamID = streamID
		i, _ := sesh.Obfs(f, obfsBuf)
		streamID++
		writingEnd.Write(obfsBuf[:i])
		time.Sleep(100 * time.Microsecond)
		stream, _ := sesh.Accept()
		stream.Close()
		i, err := stream.Read(buf)
		if err != nil {
			t.Error("failed to read", err)
		}
		if i != smallPayloadLen {
			t.Errorf("expected read %v, got %v", smallPayloadLen, i)
		}
		if !bytes.Equal(buf[:i], testPayload) {
			t.Error("expected", testPayload,
				"got", buf[:i])
		}
		_, err = stream.Read(buf)
		if err == nil {
			t.Error("expecting error", ErrBrokenStream,
				"got nil error")
		}
	})
	t.Run("Read after session close", func(t *testing.T) {
		f.StreamID = streamID
		i, _ := sesh.Obfs(f, obfsBuf)
		streamID++
		writingEnd.Write(obfsBuf[:i])
		time.Sleep(100 * time.Microsecond)
		stream, _ := sesh.Accept()
		sesh.Close()
		i, err := stream.Read(buf)
		if err != nil {
			t.Error("failed to read", err)
		}
		if i != smallPayloadLen {
			t.Errorf("expected read %v, got %v", smallPayloadLen, i)
		}
		if !bytes.Equal(buf[:i], testPayload) {
			t.Error("expected", testPayload,
				"got", buf[:i])
		}
		_, err = stream.Read(buf)
		if err == nil {
			t.Error("expecting error", ErrBrokenStream,
				"got nil error")
		}
	})

}
