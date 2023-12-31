package model

import (
	"infer-microservices/pkg/model/basemodel"
	"net/http"
)

type ModelStrategyContext struct {
	modelStrategy ModelStrategyInterface
}

func (m *ModelStrategyContext) SetModelStrategy(strategy ModelStrategyInterface) {
	m.modelStrategy = strategy
}

func (m *ModelStrategyContext) ModelInferSkywalking(requestId string, userId string, itemList []string, r *http.Request, createSample basemodel.CreateSampleCallBackFunc) (map[string]interface{}, error) {
	response, err := m.modelStrategy.ModelInferSkywalking(requestId, userId, itemList, r, createSample)
	return response, err
}

func (m *ModelStrategyContext) ModelInferNoSkywalking(requestId string, userId string, itemList []string, r *http.Request, createSample basemodel.CreateSampleCallBackFunc) (map[string]interface{}, error) {
	response, err := m.modelStrategy.ModelInferNoSkywalking(requestId, userId, itemList, r, createSample)
	return response, err
}
