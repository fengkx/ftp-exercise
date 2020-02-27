package main

import (
	"bufio"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"
)

type ftpSession struct {
	pwd          string
	conn         net.Conn
	dataHost     string
	pasvListener net.Listener
	binary       bool
	isPassive    bool
}

func newFtpSession(conn net.Conn) *ftpSession {
	pwd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}

	return &ftpSession{
		conn: conn,
		pwd:  pwd,
	}
}

// normalize to RFC format
func ftpHostNormalize(ftpHost string) (addr string, err error) {
	var a, b, c, d byte
	var p1, p2 int
	_, err = fmt.Sscanf(ftpHost, "%d,%d,%d,%d,%d,%d", &a, &b, &c, &d, &p1, &p2)
	if err != nil {
		return "", err
	}
	ip := fmt.Sprintf("%d.%d.%d.%d:%d", a, b, c, d, p1<<8+p2)
	return ip, err
}

// convert from the format in RFC
func hostToFtpHost(addr string) (ftpHost string, err error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	ip, err := net.ResolveIPAddr("ip4", host)
	if err != nil {
		return "", err
	}
	port, err := strconv.ParseInt(portStr, 10, 16)
	if err != nil {
		return "", err
	}
	ipBytes := ip.IP.To4()
	return fmt.Sprintf("%d,%d,%d,%d,%d,%d", ipBytes[0], ipBytes[1], ipBytes[2], ipBytes[3], port>>8, port%256), nil
}

func getActualPath(pwd string, dirPath string) string {
	if !path.IsAbs(dirPath) {
		return path.Join(pwd, dirPath)
	}
	return dirPath
}

func (ftp *ftpSession) writeln(msg string) {
	fmt.Fprintf(ftp.conn, "%s\r\n", msg)
}

func (ftp *ftpSession) user(username string) {
	ftp.writeln("230 User logged in, proceed.")
}

func (ftp *ftpSession) quit() {
	ftp.writeln("221 Bye")
}

func (ftp *ftpSession) port(args []string) {
	if len(args) != 1 {
		ftp.writeln("501 Usage: PORT a,b,c,d,p1,p2")
		return
	}
	var err error
	ftp.dataHost, err = ftpHostNormalize(args[0])
	if err != nil {
		ftp.writeln("501 Can't parse address.")
		return
	}
	ftp.isPassive = false
	ftp.writeln("200 PORT success")
}

func (ftp *ftpSession) typeCmd(args []string) {
	fmt.Println(args, len(args))
	if len(args) < 1 || len(args) > 2 {
		ftp.writeln("500 Usage: TYPE A")
	}
	arg := strings.Join(args, " ")
	switch arg {
	case "A", "A N":
		ftp.binary = false

	case "I", "L 8":
		ftp.binary = true
	default:
		ftp.writeln("502 only support A or I")
		return
	}
	ftp.writeln("200 TYPE success")
}

func (ftp *ftpSession) mode(args []string) {
	if len(args) != 1 {
		ftp.writeln("501 Usage: STRU F")
		return
	}
	arg := args[0]
	switch arg {
	case "S":
		ftp.writeln("200 MODE STREAM SET")
	default:
		ftp.writeln("502 only support STREAM MODE")
	}
}

func (ftp *ftpSession) stru(args []string) {
	if len(args) != 1 {
		ftp.writeln("501 Usage: STRU F")
		return
	}
	arg := args[0]
	switch arg {
	case "F":
		ftp.writeln("200 STRU file set")
	default:
		ftp.writeln("502 only support STRU FILE")
	}
}

func (ftp *ftpSession) pasv() {
	var err error
	ftp.pasvListener, err = net.Listen("tcp4", "")
	if err != nil {
		ftp.writeln("425 Can't open data connection.")
		return
	}
	addr := ftp.pasvListener.Addr().String()
	var portStr string
	_, portStr, err = net.SplitHostPort(addr)
	if err != nil {
		ftp.writeln("421 Listner error.")
		return
	}
	localAddr := ftp.conn.LocalAddr().String()
	var ip string
	ip, _, err = net.SplitHostPort(localAddr)
	if err != nil {
		ftp.writeln("421 Listner error.")
		return
	}
	var ftpHost string
	ftpHost, err = hostToFtpHost(fmt.Sprintf("%s:%s", ip, portStr))
	if err != nil {
		ftp.writeln("421 Listner error.")
		return
	}
	ftp.isPassive = true
	ftp.writeln(fmt.Sprintf("227 Entering Passive Mode %s", ftpHost))
}

func (ftp *ftpSession) retr(filePath string) {
	var err error
	var c io.ReadWriteCloser
	filePath = path.Join(ftp.pwd, filePath)
	log.Println(filePath)
	f, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			ftp.writeln("450 File not found.")
			return
		}
		ftp.writeln("450 open file error")
		return
	}
	defer f.Close()
	ftp.writeln("150 File status okay; about to open data connection")

	if ftp.isPassive {
		c, err = ftp.pasvListener.Accept()
		if err != nil {
			ftp.writeln("425 Can't open data connection")
			return
		}
		defer c.Close()
	} else {
		c, err = net.Dial("tcp4", ftp.dataHost)
		if err != nil {
			ftp.writeln("425 Can't open data connection")
			return
		}
		defer c.Close()
	}

	ftp.writeln("125 Data connection already open; transfer starting")
	_, err = io.Copy(c, f)
	if err != nil {
		ftp.writeln("450 File transfer error.")
		return
	}
	ftp.writeln("226 file transfer")
}

func (ftp *ftpSession) stor(filePath string) {
	var c io.ReadWriteCloser
	var err error
	if ftp.isPassive {
		c, err = ftp.pasvListener.Accept()
		if err != nil {
			ftp.writeln("425 Can't open data connection")
			return
		}
		defer c.Close()
	} else {
		c, err = net.Dial("tcp4", ftp.dataHost)
		if err != nil {
			log.Fatal(err)
			ftp.writeln("425 Can't open data connection")
			return
		}
		defer c.Close()
	}

	ftp.writeln("125 Data connection already open; transfer starting")
	file, err := ioutil.ReadAll(c)
	if err != nil {
		log.Fatal(err)
		ftp.writeln("450 File transfer error")
		return
	}
	filePath = path.Join(ftp.pwd, filePath)
	var saveFile *os.File

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		saveFile, err = os.Create(filePath)
	}
	if err != nil {
		log.Fatal(err)
		ftp.writeln("450 File transfer error")
		return
	}
	saveFile.Write(file)
	saveFile.Close()
	tf, _ := os.Stat(filePath)
	sz := strconv.FormatInt(tf.Size(), 10)
	log.Println(filePath + " [" + sz + "]" + "bytes")
	ftp.writeln("226 file transfer")
}

func (ftp *ftpSession) pwdCmd(args []string) {
	if len(args) != 0 {
		ftp.writeln("501 Syntax Error of argument")
		return
	}
	ftp.writeln("257 " + "\"" + ftp.pwd + "\"" + " is current directory")
}

func (ftp *ftpSession) cwdCmd(dirPath string) {
	dirPath = getActualPath(ftp.pwd, dirPath)
	_, err := os.Stat(dirPath)
	if err != nil {
		if os.IsNotExist(err) {
			ftp.writeln("550 File Not found")
		}
		ftp.writeln("550 File Not accessable")
		return
	}
	ftp.pwd = dirPath
	ftp.writeln("250 directory changed to \"" + ftp.pwd + "\"")
}

func (ftp *ftpSession) list(args []string) {
	var p string
	if len(args) == 0 {
		p = ftp.pwd
	} else {
		p = path.Join(ftp.pwd, args[1])
	}
	var c io.ReadWriteCloser
	var err error
	if ftp.isPassive {
		c, err = ftp.pasvListener.Accept()
		if err != nil {
			ftp.writeln("425 Can't open data connection")
			return
		}
		defer c.Close()
	} else {
		c, err = net.Dial("tcp4", ftp.dataHost)
		if err != nil {
			log.Fatal(err)
			ftp.writeln("425 Can't open data connection")
			return
		}
		defer c.Close()
	}

	ftp.writeln("150 start sending file list data")

	cmd := exec.Command("ls", "-la", p)
	out, err := cmd.Output()
	if err != nil {
		ftp.writeln("450 file system error")
		fmt.Println(err)
		return
	}
	d := string(out)
	lines := strings.Split(d, "\n")
	d = strings.Join(lines, "\r\n")
	c.Write([]byte(d))
	ftp.writeln("226 Transfer completed")
	c.Close()
	time.Sleep(1)
}

func main() {
	listener, err := net.Listen("tcp4", "localhost:8000")
	if err != nil {
		log.Fatal(err)
	}
	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Fatal(err)
			continue
		}
		log.Println("accept")
		go handleConn(conn)
	}
}

func handleConn(c net.Conn) {
	session := newFtpSession(c)
	session.writeln("220 Service ready")
	input := bufio.NewScanner(c)
	for input.Scan() {
		text := input.Text()
		fmt.Println("*: ", text)
		cuts := strings.Fields(text)
		if len(cuts) <= 0 {
			continue
		}
		cmd := strings.ToUpper(cuts[0])
		args := cuts[1:]
		switch cmd {
		case "USER":
			session.user(args[0])
		case "QUIT":
			session.quit()
		case "TYPE":
			session.typeCmd(args)
		case "MODE":
			session.mode(args)
		case "STRU":
			session.stru(args)
		case "PASV":
			session.pasv()
		case "PORT":
			session.port(args)
		case "RETR":
			session.retr(args[0])
		case "STOR":
			session.stor(args[0])
		case "NOOP":
			session.writeln("200 Okay")
		case "PWD":
			session.pwdCmd(args)
		case "CWD":
			session.cwdCmd(args[0])
		case "LIST":
			session.list(args)
		default:
			session.writeln("502 command not support")
		}
	}
}
