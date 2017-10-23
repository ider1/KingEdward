package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"flag"
	"errors"
	//"strings"
	"container/list"
	"encoding/json"

	"github.com/cmu440/lsp"
	"github.com/cmu440/bitcoin"
)

const (
	AVAILABLE = 1
	INUSE     = 0
        MINER     = 0
        REQUEST   = 1
	taskPerWorker = 2000
)

var (
	epochLimit  = flag.Int("elim", lsp.DefaultEpochLimit, "epoch limit")
        epochMillis = flag.Int("ems", lsp.DefaultEpochMillis, "epoch duration (ms)")
        windowSize  = flag.Int("wsize", lsp.DefaultWindowSize, "window size")
        maxBackoff  = flag.Int("maxbackoff", lsp.DefaultMaxBackOffInterval, "maximum interval epoch")
	LOGF *log.Logger
)

type Result struct {
	Hash	uint64
	Nonce   uint64
}

type Request struct {
	Id		int
	Data		string
	Lower		uint64
	Upper		uint64
	Conn		int
	totalUnits	int
	finishUnits	int
	workers		map[int]Result
}

type Miner struct {
	Id	int
	Conn	int
	Status  int
	Req	int
	Task	[]byte
}

type server struct {
	lspServer	lsp.Server
	requests	map[int]Request
	miners		map[int]Miner
	availMiners	*list.List
        bufferTasks     *list.List
	reqCount	int
	minCount	int
}

type Task struct {
	Req	int
	Msg	[]byte
}

func startServer(port int) (*server, error) {
	LOGF.Printf("[start] Start Server Begin!")
	params := &lsp.Params{
                EpochLimit:         *epochLimit,
                EpochMillis:        *epochMillis,
                WindowSize:         *windowSize,
                MaxBackOffInterval: *maxBackoff,
        }
	portFlag := flag.Int("port", port, "port number")
	srv, err := lsp.NewServer(*portFlag, params)
        if err != nil {
                //fmt.Printf("Failed to start Server on port %d: %s\n", port, err)
                return nil,errors.New("Cannot start server, Please Check parameter configuration")
        }

	requests := make(map[int]Request)
	miners   := make(map[int]Miner)
	availMiners := list.New()
	bufferTasks  := list.New()
	reqCount := 0
	minCount := 0
	s := &server{srv,requests,miners,availMiners,bufferTasks,reqCount,minCount}
	LOGF.Printf("[start] Start Server Finish!")
	return s,nil
}

func initLogger()(*log.Logger){
	const (
		name = "log.txt"
		flag = os.O_RDWR | os.O_CREATE
		perm = os.FileMode(0666)
	)
	file, err := os.OpenFile(name, flag, perm)
	if err != nil {
		return nil
	}
	defer file.Close()
	return log.New(file, "", log.Lshortfile|log.Lmicroseconds)
}

func readParameter()(int){
	const numArgs = 2
	if len(os.Args) != numArgs {
		//fmt.Printf("Usage: ./%s <port>", os.Args[0])
		return -1
	}

	port, err := strconv.Atoi(os.Args[1])
	if err != nil {
		fmt.Println("Port must be a number:", err)
		return -1
	}
	return port
}

func extractInfo(payload []byte) *bitcoin.Message {
	var msg = new(bitcoin.Message)
	err := json.Unmarshal(payload,msg)
	if err!=nil {
		LOGF.Printf("Error: Message Payload cannot be unmarshalled!\n")
	}
	if msg.Data==""&&msg.Upper==0&&msg.Hash==0{
		LOGF.Printf("Error: Unmarshalled Message is empty")
        }
	return msg
}

func addRequest(srv *server,m *bitcoin.Message,id int ) *Request{
        //1. Initialize req.worker
	workers := make(map[int]Result)
        //2. Initialize req.totalunits
	taskCount := int(m.Upper - m.Lower + 1)
	workersCount := taskCount/taskPerWorker
	if taskCount- taskPerWorker*workersCount > 0 {
		workersCount = workersCount + 1
	}
        //3. Initialize other parts of req and put it into requests map
	req := Request{srv.reqCount,m.Data,m.Lower,m.Upper,id,workersCount,0,workers}
	srv.requests[req.Conn] = req
	srv.reqCount = srv.reqCount + 1
	return &req
}

func addMiner(srv *server, id int){
	miner := Miner{srv.minCount,id,AVAILABLE,-1,nil}
	srv.miners[miner.Conn] = miner
	srv.minCount = srv.minCount + 1
	srv.availMiners.PushBack(miner.Conn)
}

func scheduleMiner(srv *server, req *Request) {
  minersCount := srv.availMiners.Len()
  var i int
  for i=0;i<req.totalUnits;i++ {
    //Calculate the task range first
    lo := int(req.Lower) + i * taskPerWorker
    hi := int(req.Lower) + (i+1) * taskPerWorker
    if i==req.totalUnits-1{
      hi = int(req.Upper) + 1
    }
    //If there is available miner, then use it as worker
    if (i<minersCount){
      m           := bitcoin.NewRequest(req.Data,uint64(lo),uint64(hi))
      conn        := srv.availMiners.Front()
      miner       := srv.miners[conn.Value.(int)]
      miner.Req    = req.Conn
      miner.Status = INUSE
      miner.Task,_ = json.Marshal(m)
      srv.miners[conn.Value.(int)] = miner
      fmt.Printf("[%d:%d] Task (%d,%d)Sceduled Available Miner:(%d,%d)\n",req.Id,req.Conn,lo,hi,miner.Id,miner.Conn)
      srv.lspServer.Write(miner.Conn,miner.Task)
      srv.availMiners.Remove(conn)
    } else{
    //If there is no available miner, then put it into bufferTasks
      m		:= bitcoin.NewRequest(req.Data,uint64(lo),uint64(hi))
      msg,_	:= json.Marshal(m)
      task	:= Task{req.Conn,msg}
      fmt.Printf("[%d:%d] Task (%d,%d)Sceduled into Buffer Task Queue\n",req.Id,req.Conn,lo,hi)
      srv.bufferTasks.PushBack(task)
    }
  }

  return
}


func updateRequest(srv *server,m *bitcoin.Message, conn int){
  miner := srv.miners[conn]
  req,existFlag := srv.requests[miner.Req]
  tmp := miner.Req
  miner.Req = -1
  miner.Status = AVAILABLE
  miner.Task = nil
  srv.miners[conn] = miner
  srv.availMiners.PushBack(conn)
  if !existFlag {
    return
  }
  req.workers[req.finishUnits] = Result{m.Hash,m.Nonce}
  req.finishUnits = req.finishUnits + 1
  fmt.Printf("[update] Miner(%d:%d) send a result to Request(%d:%d)[%d,%d]\n",miner.Id,miner.Conn,req.Id,req.Conn,req.finishUnits,req.totalUnits)
  srv.requests[tmp] = req
  var resultHash uint64
  var resultNonce uint64
  if req.finishUnits==req.totalUnits {
    for _, v := range req.workers {
      //fmt.Printf("[Result] Result (%s,%s) of Request (%d,%d)\n",strconv.FormatUint(v.Hash,10),strconv.FormatUint(v.Nonce,10),req.Id,req.Conn)
      if resultHash==0 {
        resultHash = v.Hash
        resultNonce = v.Nonce
      } else if v.Hash<resultHash{
        resultHash = v.Hash
        resultNonce = v.Nonce
      }
    }
    fmt.Printf("[Result] Return result (%s,%s) to client (%d,%d)\n",strconv.FormatUint(resultHash,10),strconv.FormatUint(resultNonce,10),req.Id,req.Conn)
    _,ok := srv.requests[req.Conn]
    if !ok {
      LOGF.Printf("[%d:%d] Request Connection Has Lost",req.Id,req.Conn)
      return
    }
    m	:= bitcoin.NewResult(resultHash,resultNonce)
    msg,_ := json.Marshal(m)
    //fmt.Printf("[Result] Write to Request Client:%s\n",msg)
    srv.lspServer.Write(req.Conn,msg)
    delete(srv.requests,req.Conn)
    return
  }
  return
}

func judgeLoss(srv *server,conn int) int {
  _,ok := srv.miners[conn]
  if ok {
    return MINER
  } else {
    return REQUEST
  }
}

func readRoutine(srv *server){
  for{
    //fmt.Printf("[read] Server Waiting for msg...\n")
    if id,payload,err := srv.lspServer.Read();err!=nil{
      LOGF.Printf("[read] Client %d has died: %s\n",id,err)
      switch judgeLoss(srv,id){
      case MINER:
        //fmt.Printf("[%d] Miner Connection Lost\n",id)
        miner := srv.miners[id]
        delete(srv.miners,id)
        flag := true
        //If the lost miner is an available miner
        for iter := srv.availMiners.Front();iter != nil ;iter = iter.Next() {
          if (iter.Value.(int)==id){
            srv.availMiners.Remove(iter)
            flag = false
            break
          }
        }
        //If the lost miner is a worker miner
        if flag {
          task := Task{miner.Req,miner.Task}
	  srv.bufferTasks.PushBack(task)
        }
      case REQUEST:
	//fmt.Printf("[%d] Request Connection Lost\n",id)
        delete(srv.requests,id)
      }
    } else {
      //LOGF.Printf("[read] Server received '%s' from client %d\n",string(payload),id)
      m := extractInfo(payload)
      //fmt.Printf("[read] Receive Message "+m.String()+"\n")
      switch m.Type{

      case bitcoin.Request:
        //LOGF.Printf("[%d] Server begin dealing REQUEST message\n",id)
	req := addRequest(srv,m,id)
	scheduleMiner(srv,req)

      case bitcoin.Join:
        //LOGF.Printf("[%d] Server begin dealing REQUEST message\n",id)
        addMiner(srv,id)
        for srv.availMiners.Len() > 0 {
          //fmt.Printf("[server] Server bufferTask Number:%d\n",srv.bufferTasks.Len())
          if (srv.bufferTasks.Len()!=0){
            //1. Fetch the task from bufferTasks
            taskele := srv.bufferTasks.Front()
            srv.bufferTasks.Remove(taskele)
            task  := taskele.Value.(Task)
            //2. Fetch the miner from availMiners
            conn        := srv.availMiners.Front()
            miner       := srv.miners[conn.Value.(int)]
            miner.Req    = task.Req
            miner.Status = INUSE
            miner.Task   = task.Msg
            srv.miners[conn.Value.(int)] = miner
            srv.lspServer.Write(miner.Conn,miner.Task)
            srv.availMiners.Remove(conn)
          } else {
            break
          }
        }

      case bitcoin.Result:
        //LOGF.Printf("[%d] Server begin dealing RESULT message\n",id)
	updateRequest(srv,m,id)
        for srv.availMiners.Len() > 0 {
          //fmt.Printf("[server] Server bufferTask Number:%d\n",srv.bufferTasks.Len())
          if (srv.bufferTasks.Len()!=0){
            //1. Fetch the task from bufferTasks
            taskele := srv.bufferTasks.Front()
            srv.bufferTasks.Remove(taskele)
            task  := taskele.Value.(Task)
            //2. Fetch the miner from availMiners
            conn	:= srv.availMiners.Front()
            miner	:= srv.miners[conn.Value.(int)]
            miner.Req	 = task.Req
            miner.Status = INUSE
            miner.Task   = task.Msg
            srv.miners[conn.Value.(int)] = miner
            srv.lspServer.Write(miner.Conn,miner.Task)
            srv.availMiners.Remove(conn)
          } else {
            break
          }
        }
      default:
        LOGF.Printf("[%d] Message Type Could not be identified\n",id)
	continue
      }
    }
  }
}

func main() {
	port := readParameter()
	LOGF = initLogger()
	srv, err := startServer(port)
	if err != nil {
		fmt.Println(err.Error())
		return
	}
	fmt.Println("Server listening on port", port)
	defer srv.lspServer.Close()
	readRoutine(srv)
}
