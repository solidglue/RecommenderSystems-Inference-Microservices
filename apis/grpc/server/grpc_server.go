package server

import (
	"errors"
	"fmt"
	"infer-microservices/common/flags"
	"infer-microservices/cores/nacos_config"
	"infer-microservices/cores/service_config"
	"time"

	"infer-microservices/apis"
	grpc_api "infer-microservices/apis/grpc/server/api_gogofaster"
	"infer-microservices/apis/input_format"

	"infer-microservices/utils/logs"
	"net"
	"runtime"

	"github.com/afex/hystrix-go/hystrix"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
)

var grpcListenPort uint
var maxCpuNum int
var skywalkingWeatherOpen bool
var lowerRecallNum int
var lowerRankNum int
var ipAddr_ string
var port_ uint64
var grpcInfer grpcInferInterface

// server is used to implement customer.CustomerServer.
type grpcRecommender struct {
}

func init() {
	flagFactory := flags.FlagFactory{}
	flagServiceConfig := flagFactory.FlagServiceConfigFactory()
	flagSkywalking := flagFactory.FlagSkywalkingFactory()
	flagHystrix := flagFactory.FlagHystrixFactory()

	grpcListenPort = *flagServiceConfig.GetServiceGrpcPort()
	maxCpuNum = *flagServiceConfig.GetServiceMaxCpuNum()
	skywalkingWeatherOpen = *flagSkywalking.GetSkywalkingWhetheropen()
	lowerRecallNum = *flagHystrix.GetHystrixLowerRecallNum()
	lowerRankNum = *flagHystrix.GetHystrixLowerRankNum()
}

// INFO: implement grpc func which defined by proto.
func (g *grpcRecommender) GrpcRecommendServer(ctx context.Context, in *grpc_api.RecommendRequest) (*grpc_api.RecommendResponse, error) {

	//INFO: set timeout by context, degraded service by hystix.
	resp_info := &grpc_api.RecommendResponse{
		Code: 404,
	}

	ctx, cancelFunc := context.WithTimeout(ctx, time.Millisecond*150)
	defer cancelFunc()

	respCh := make(chan *grpc_api.RecommendResponse, 100)
	go g.grpcRecommenderServerContext(ctx, in, respCh)
	for {
		select {
		case <-ctx.Done():
			switch ctx.Err() {
			case context.DeadlineExceeded:
				return resp_info, ctx.Err()
			case context.Canceled:
				return resp_info, ctx.Err()
			}
		case responseCh := <-respCh:
			resp_info = responseCh
			return resp_info, nil
		}
	}
}

func (g *grpcRecommender) grpcRecommenderServerContext(ctx context.Context, in *grpc_api.RecommendRequest, respCh chan *grpc_api.RecommendResponse) {

	defer func() {
		if info := recover(); info != nil {
			fmt.Println("panic", info)

		} //else {
		//fmt.Println("")
		//}
	}()

	resp_info := &grpc_api.RecommendResponse{
		Code: 404,
	}

	dataId := in.DataId
	groupId := in.GroupId
	namespaceId := in.Namespace

	request := apis.RecRequest{}
	request.SetDataId(dataId)
	request.SetGroupId(groupId)
	request.SetNamespaceId(namespaceId)
	request.SetUserId(in.UserId)
	request.SetRecallNum(in.RecallNum)
	request.SetItemList(in.ItemList.Value)

	ServiceConfig := apis.ServiceConfigs[dataId]

	nacosConn := nacos_config.NacosConnConfig{}
	nacosConn.SetDataId(dataId)
	nacosConn.SetGroupId(groupId)
	nacosConn.SetNamespaceId(namespaceId)
	nacosConn.SetIp(ipAddr_)
	nacosConn.SetPort(port_)

	_, ok := apis.NacosListedMap[dataId]
	if !ok {
		err := nacosConn.ServiceConfigListen()
		if err != nil {
			return
		} else {
			apis.NacosListedMap[dataId] = true
		}
	}

	response_, err := g.grpcHystrixServer("grpcServer", &request, ServiceConfig)
	if err != nil {
		resp_info.Message = fmt.Sprintf("%s", err)
		return
	} else {
		resp_info = response_
	}
	respCh <- resp_info
}

func (r *grpcRecommender) grpcHystrixServer(serverName string, in *apis.RecRequest, ServiceConfig *service_config.ServiceConfig) (*grpc_api.RecommendResponse, error) {
	resp_info := &grpc_api.RecommendResponse{
		Code: 404,
	}

	hystrix.Do(serverName, func() error {
		// request recall / rank func.
		response_, err := r.grpcRecommender(in, ServiceConfig)
		if err != nil {
			logs.Error(err)
		} else {
			resp_info = response_
		}

		return nil
	}, func(err error) error {
		// do this when services are timeout.
		if err != nil {
			logs.Error(err)
		}

		itemList := in.GetItemList()
		in.SetRecallNum(int32(lowerRecallNum))
		in.SetItemList(itemList[:lowerRankNum])
		response_, err := r.grpcRecommender(in, ServiceConfig)

		if err != nil {
			logs.Error(err)
		} else {
			resp_info = response_
		}

		return nil
	})

	return resp_info, nil
}

func (g *grpcRecommender) grpcRecommender(in *apis.RecRequest, ServiceConfig *service_config.ServiceConfig) (*grpc_api.RecommendResponse, error) {
	resp_info := &grpc_api.RecommendResponse{
		Code: 404,
	}

	request := apis.RecRequest{}
	recaller := input_format.RecallInputFormat{}
	ranker := input_format.RankInputFormat{}
	modelType := in.GetModelType()
	if modelType == "recall" {
		dssm, err := recaller.InputCheckAndFormat(&request, ServiceConfig)
		if err != nil {
			logs.Error(err)
			return nil, err
		}
		grpcInfer = &recallServer{dssm}
		resp_info, err = grpcInfer.grpcInferServer()
		if err != nil {
			logs.Error(err)
			return nil, err
		}
	} else if modelType == "rank" {
		deepfm, err := ranker.InputCheckAndFormat(&request, ServiceConfig)
		if err != nil {
			logs.Error(err)
			return nil, err
		}

		grpcInfer = &rankServer{deepfm}
		resp_info, err = grpcInfer.grpcInferServer()
		if err != nil {
			logs.Error(err)
			return nil, err
		}
	} else {
		return resp_info, errors.New("wrong Strategy")
	}

	return resp_info, nil
}

func GrpcServerRunner(nacosIp string, nacosPort uint64) error {
	ipAddr_ = nacosIp
	port_ = nacosPort
	cpuNum := runtime.NumCPU()
	if maxCpuNum <= cpuNum {
		cpuNum = maxCpuNum
	}
	runtime.GOMAXPROCS(cpuNum)
	logs.Debug("cup num:", cpuNum)

	addr := fmt.Sprintf(":%d", grpcListenPort)
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		logs.Fatal("failed to listen: %v", err)
	} else {
		logs.Debug("listen to port:", addr)

	}

	s := grpc.NewServer()
	grpc_api.RegisterGrpcRecommendServerServiceServer(s, nil)
	s.Serve(lis)
	if err != nil {
		logs.Error(err)
		return err
	}

	return nil
}