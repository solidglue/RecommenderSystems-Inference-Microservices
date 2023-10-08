package server

import (
	"context"
	"errors"
	"fmt"
	"infer-microservices/apis"
	"infer-microservices/apis/input_format"
	"infer-microservices/common/flags"
	"infer-microservices/cores/nacos_config"
	"infer-microservices/cores/service_config"
	"infer-microservices/utils/logs"
	"time"

	"dubbo.apache.org/dubbo-go/v3/config"
	_ "dubbo.apache.org/dubbo-go/v3/imports"
	"github.com/afex/hystrix-go/hystrix"
	hessian "github.com/apache/dubbo-go-hessian2"
)

var ipAddr_ string
var port_ uint64
var lowerRecallNum int
var lowerRankNum int
var dubboInfer dubboInferInterface

type DubbogoInferService struct {
}

func init() {
	//regisger dubbo service.
	hessian.RegisterPOJO(&apis.RecRequest{})
	hessian.RegisterPOJO(&apis.RecResponse{})
	config.SetProviderService(&DubbogoInferService{})

	//get hystrix parms.
	flagFactory := flags.FlagFactory{}
	flagHystrix := flagFactory.FlagHystrixFactory()
	lowerRecallNum = *flagHystrix.GetHystrixLowerRecallNum()
	lowerRankNum = *flagHystrix.GetHystrixLowerRankNum()

}

//INFO:DONT REMOVE.
// // MethodMapper mapper upper func name to lower func name ,for java request.
// func (s *InferDubbogoService) MethodMapper() map[string]string {
// 	return map[string]string{
// 		"DubboRecommendServer": "dubboRecommendServer",
// 	}
// }

// Implement interface methods.
func (r *DubbogoInferService) DubboRecommendServer(ctx context.Context, in *apis.RecRequest) (*apis.RecResponse, error) {
	response := &apis.RecResponse{}
	response.SetCode(404)

	//INFO: set timeout by context, degraded service by hystix.
	ctx, cancelFunc := context.WithTimeout(ctx, time.Millisecond*100)
	defer cancelFunc()

	respCh := make(chan *apis.RecResponse, 100)
	go r.dubboRecommenderServerContext(ctx, in, respCh)

	for {
		select {
		case <-ctx.Done():
			switch ctx.Err() {
			case context.DeadlineExceeded:
				logs.Info("context timeout DeadlineExceeded.")
				return response, ctx.Err()
			case context.Canceled:
				logs.Info("context timeout Canceled.")
				return response, ctx.Err()
			}
		case responseCh := <-respCh:
			response = responseCh
			return response, nil
		}
	}
}

func (r *DubbogoInferService) dubboRecommenderServerContext(ctx context.Context, in *apis.RecRequest, respCh chan *apis.RecResponse) {
	response := &apis.RecResponse{}
	response.SetCode(404)

	dataId := in.GetDataId()
	groupId := in.GetGroupId()
	namespaceId := in.GetNamespaceId()
	ServiceConfig := apis.ServiceConfigs[in.GetDataId()]

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

	response_, err := r.dubboHystrixServer("dubboServer", in, ServiceConfig)
	if err != nil {
		response.SetMessage(fmt.Sprintf("%s", err))
		return
	} else {
		response = response_
	}

	respCh <- response
}

func (r *DubbogoInferService) dubboHystrixServer(serverName string, in *apis.RecRequest, ServiceConfig *service_config.ServiceConfig) (*apis.RecResponse, error) {
	defer func() {
		if info := recover(); info != nil {
			fmt.Println("panic", info)
		} //else {
		//  fmt.Println("finish.")
		//}
	}()

	response := &apis.RecResponse{}
	response.SetCode(404)

	hystrix.Do(serverName, func() error {
		// request recall / rank func.
		response_, err := r.dubboRecommender(in, ServiceConfig)
		if err != nil {
			logs.Error(err)
		} else {
			response = response_
		}
		return nil
	}, func(err error) error {
		//INFO: do this when services are timeout (hystrix timeout).
		if err != nil {
			logs.Error(err)
		}

		itemList := in.GetItemList()
		in.SetRecallNum(int32(lowerRecallNum))
		in.SetItemList(itemList[:lowerRankNum])
		response_, err := r.dubboRecommender(in, ServiceConfig)

		if err != nil {
			logs.Error(err)
		} else {
			response = response_
		}

		return nil
	})

	return response, nil
}

func (r *DubbogoInferService) dubboRecommender(in *apis.RecRequest, ServiceConfig *service_config.ServiceConfig) (*apis.RecResponse, error) {
	response := &apis.RecResponse{}
	response.SetCode(404)

	dataId := in.GetDataId()
	groupId := in.GetGroupId()
	namespaceId := in.GetNamespaceId()
	userId := in.GetUserId()
	itemList := in.GetItemList()

	request := apis.RecRequest{}
	request.SetDataId(dataId)
	request.SetGroupId(groupId)
	request.SetNamespaceId(namespaceId)
	request.SetUserId(userId)
	request.SetItemList(itemList)

	modelType := in.GetModelType()
	if modelType == "recall" {
		recaller := input_format.RecallInputFormat{}
		dssm, err := recaller.InputCheckAndFormat(&request, ServiceConfig)
		if err != nil {
			logs.Error(err)
			return response, err
		}
		dubboInfer = &recallServer{dssm}
		response_, err := dubboInfer.dubboInferServer()
		if err != nil {
			logs.Error(err)
			return response, err
		} else {
			response = response_
		}
	} else if modelType == "rank" {
		ranker := input_format.RankInputFormat{}
		deepfm, err := ranker.InputCheckAndFormat(&request, ServiceConfig)
		if err != nil {
			logs.Error(err)
			return response, err
		}

		dubboInfer = &rankServer{deepfm}
		response_, err := dubboInfer.dubboInferServer()
		if err != nil {
			logs.Error(err)
			return response, err
		} else {
			response = response_
		}
	} else {
		err := errors.New("wrong Strategy")
		return response, err
	}

	return response, nil
}

func DubboServerRunner(ipAddr string, port uint64, dubboConfFile string) {
	ipAddr_ = ipAddr
	port_ = port
	if err := config.Load(config.WithPath(dubboConfFile)); err != nil {
		panic(err)
	}
}