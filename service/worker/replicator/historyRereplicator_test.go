// Copyright (c) 2017 Uber Technologies, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package replicator

import (
	"os"
	"testing"
	"time"

	"github.com/pborman/uuid"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
	"github.com/uber-common/bark"
	"github.com/uber-go/tally"
	"github.com/uber/cadence/.gen/go/history"
	internal "github.com/uber/cadence/.gen/go/shared"
	"github.com/uber/cadence/common"
	"github.com/uber/cadence/common/cache"
	"github.com/uber/cadence/common/cluster"
	"github.com/uber/cadence/common/metrics"
	"github.com/uber/cadence/common/mocks"
	"github.com/uber/cadence/common/persistence"
	external "go.uber.org/cadence/.gen/go/shared"
)

type (
	historyRereplicatorSuite struct {
		suite.Suite

		domainID   string
		domainName string

		mockClusterMetadata *mocks.ClusterMetadata
		mockMetadataMgr     *mocks.MetadataManager
		mockFrontendClient  *mocks.FrontendClient
		mockHistoryClient   *mocks.HistoryClient
		serializer          persistence.HistorySerializer
		logger              bark.Logger

		rereplicator *HistoryRereplicatorImpl
	}
)

func TestHistoryRereplicatorSuite(t *testing.T) {
	s := new(historyRereplicatorSuite)
	suite.Run(t, s)
}

func (s *historyRereplicatorSuite) SetupSuite() {
	if testing.Verbose() {
		log.SetOutput(os.Stdout)
	}
}

func (s *historyRereplicatorSuite) TearDownSuite() {

}

func (s *historyRereplicatorSuite) SetupTest() {
	log2 := log.New()
	log2.Level = log.DebugLevel
	s.logger = bark.NewLoggerFromLogrus(log2)
	s.mockClusterMetadata = &mocks.ClusterMetadata{}
	s.mockClusterMetadata.On("IsGlobalDomainEnabled").Return(true)
	s.mockMetadataMgr = &mocks.MetadataManager{}

	s.domainID = uuid.New()
	s.domainName = "some random domain name"
	s.mockMetadataMgr.On("GetDomain", mock.Anything).Return(
		&persistence.GetDomainResponse{
			Info:   &persistence.DomainInfo{ID: s.domainID, Name: s.domainName},
			Config: &persistence.DomainConfig{Retention: 1},
			ReplicationConfig: &persistence.DomainReplicationConfig{
				ActiveClusterName: cluster.TestCurrentClusterName,
				Clusters: []*persistence.ClusterReplicationConfig{
					&persistence.ClusterReplicationConfig{ClusterName: cluster.TestCurrentClusterName},
				},
			},
			TableVersion: persistence.DomainTableVersionV1,
		}, nil,
	)
	s.mockFrontendClient = &mocks.FrontendClient{}
	s.mockHistoryClient = &mocks.HistoryClient{}
	s.serializer = persistence.NewHistorySerializer()
	metricsClient := metrics.NewClient(tally.NoopScope, metrics.History)
	domainCache := cache.NewDomainCache(s.mockMetadataMgr, s.mockClusterMetadata, metricsClient, s.logger)
	s.rereplicator = NewHistoryRereplicator(
		domainCache,
		s.mockFrontendClient,
		s.mockHistoryClient,
		persistence.NewHistorySerializer(),
		s.logger,
	)
}

func (s *historyRereplicatorSuite) TearDownTest() {
	s.mockFrontendClient.AssertExpectations(s.T())
	s.mockHistoryClient.AssertExpectations(s.T())
}

func (s *historyRereplicatorSuite) TestSendMultiWorkflowHistory_SameRunID() {
	workflowID := "some random workflow ID"
	runID := uuid.New()
	firstEventID := int64(123)
	nextEventID := firstEventID + 100
	branchToken := []byte("some random branch token")
	pageSize := int32(100)
	replicationInfo := map[string]*external.ReplicationInfo{
		"random data center": &external.ReplicationInfo{
			Version:     common.Int64Ptr(777),
			LastEventId: common.Int64Ptr(999),
		},
	}
	eventStoreVersion := int32(9)
	eventBatch := []*internal.HistoryEvent{
		&internal.HistoryEvent{
			EventId:   common.Int64Ptr(2),
			Version:   common.Int64Ptr(123),
			Timestamp: common.Int64Ptr(time.Now().UnixNano()),
			EventType: internal.EventTypeDecisionTaskScheduled.Ptr(),
		},
		&internal.HistoryEvent{
			EventId:   common.Int64Ptr(3),
			Version:   common.Int64Ptr(123),
			Timestamp: common.Int64Ptr(time.Now().UnixNano()),
			EventType: internal.EventTypeDecisionTaskStarted.Ptr(),
		},
	}
	blob := s.serializeEvents(eventBatch)

	s.mockFrontendClient.On("GetWorkflowExecutionRawHistory", mock.Anything, &external.GetWorkflowExecutionRawHistoryRequest{
		Domain: common.StringPtr(s.domainName),
		Execution: &external.WorkflowExecution{
			WorkflowId: common.StringPtr(workflowID),
			RunId:      common.StringPtr(runID),
		},
		BranchToken:     nil,
		FirstEventId:    common.Int64Ptr(firstEventID),
		NextEventId:     common.Int64Ptr(nextEventID),
		MaximumPageSize: common.Int32Ptr(pageSize),
		NextPageToken:   nil,
	}).Return(&external.GetWorkflowExecutionRawHistoryResponse{
		BranchToken:       branchToken,
		HistoryBatches:    []*external.DataBlob{blob},
		NextPageToken:     nil,
		ReplicationInfo:   replicationInfo,
		EventStoreVersion: common.Int32Ptr(eventStoreVersion),
	}, nil).Once()

	s.mockHistoryClient.On("ReplicateRawEvents", mock.Anything, &history.ReplicateRawEventsRequest{
		DomainUUID: common.StringPtr(s.domainID),
		WorkflowExecution: &internal.WorkflowExecution{
			WorkflowId: common.StringPtr(workflowID),
			RunId:      common.StringPtr(runID),
		},
		ReplicationInfo: s.rereplicator.replicationInfoFromPublic(replicationInfo),
		History: &internal.DataBlob{
			EncodingType: internal.EncodingTypeThriftRW.Ptr(),
			Data:         blob.Data,
		},
		NewRunHistory:           nil,
		EventStoreVersion:       common.Int32Ptr(eventStoreVersion),
		NewRunEventStoreVersion: nil,
	}).Return(nil).Once()

	err := s.rereplicator.SendMultiWorkflowHistory(s.domainID, workflowID, runID, firstEventID, runID, nextEventID)
	s.Nil(err)
}

func (s *historyRereplicatorSuite) TestSendMultiWorkflowHistory_DiffRunID_Continued() {
	workflowID := "some random workflow ID"
	branchToken := []byte("some random branch token")
	pageSize := int32(100)
	beginingEventID := int64(133)
	endingEventID := int64(20)

	// beginingRunID -> midRunID1; not continue relationship; midRunID2 -> endingRunID

	beginingRunID := "00001111-2222-3333-4444-555566661111"
	beginingEventStoreVersion := int32(101)
	beginingReplicationInfo := map[string]*external.ReplicationInfo{
		"random data center 1": &external.ReplicationInfo{
			Version:     common.Int64Ptr(111),
			LastEventId: common.Int64Ptr(222),
		},
	}

	midRunID1 := "00001111-2222-3333-4444-555566662222"
	midEventStoreVersion1 := int32(102)
	midReplicationInfo1 := map[string]*external.ReplicationInfo{
		"random data center 2": &external.ReplicationInfo{
			Version:     common.Int64Ptr(111),
			LastEventId: common.Int64Ptr(222),
		},
	}

	midRunID2 := "00001111-2222-3333-4444-555566663333"
	midEventStoreVersion2 := int32(103)
	midReplicationInfo2 := map[string]*external.ReplicationInfo{
		"random data center 3": &external.ReplicationInfo{
			Version:     common.Int64Ptr(111),
			LastEventId: common.Int64Ptr(222),
		},
	}

	endingRunID := "00001111-2222-3333-4444-555566664444"
	endingEventStoreVersion := int32(104)
	endingReplicationInfo := map[string]*external.ReplicationInfo{
		"random data center 4": &external.ReplicationInfo{
			Version:     common.Int64Ptr(777),
			LastEventId: common.Int64Ptr(888),
		},
	}

	beginingEventBatch := []*internal.HistoryEvent{
		&internal.HistoryEvent{
			EventId:   common.Int64Ptr(4),
			Version:   common.Int64Ptr(123),
			Timestamp: common.Int64Ptr(time.Now().UnixNano()),
			EventType: internal.EventTypeDecisionTaskCompleted.Ptr(),
		},
		&internal.HistoryEvent{
			EventId:   common.Int64Ptr(5),
			Version:   common.Int64Ptr(123),
			Timestamp: common.Int64Ptr(time.Now().UnixNano()),
			EventType: internal.EventTypeWorkflowExecutionContinuedAsNew.Ptr(),
			WorkflowExecutionContinuedAsNewEventAttributes: &internal.WorkflowExecutionContinuedAsNewEventAttributes{
				NewExecutionRunId: common.StringPtr(midRunID1),
			},
		},
	}
	beginingBlob := s.serializeEvents(beginingEventBatch)

	midEventBatch1 := []*internal.HistoryEvent{
		&internal.HistoryEvent{
			EventId:   common.Int64Ptr(1),
			Version:   common.Int64Ptr(123),
			Timestamp: common.Int64Ptr(time.Now().UnixNano()),
			EventType: internal.EventTypeWorkflowExecutionStarted.Ptr(),
			WorkflowExecutionStartedEventAttributes: &internal.WorkflowExecutionStartedEventAttributes{
				ContinuedExecutionRunId: common.StringPtr(beginingRunID),
			},
		},
		&internal.HistoryEvent{
			EventId:   common.Int64Ptr(5),
			Version:   common.Int64Ptr(123),
			Timestamp: common.Int64Ptr(time.Now().UnixNano()),
			EventType: internal.EventTypeWorkflowExecutionCompleted.Ptr(),
		},
	}
	midBlob1 := s.serializeEvents(midEventBatch1)

	midEventBatch2 := []*internal.HistoryEvent{
		&internal.HistoryEvent{
			EventId:   common.Int64Ptr(1),
			Version:   common.Int64Ptr(123),
			Timestamp: common.Int64Ptr(time.Now().UnixNano()),
			EventType: internal.EventTypeWorkflowExecutionStarted.Ptr(),
			WorkflowExecutionStartedEventAttributes: &internal.WorkflowExecutionStartedEventAttributes{
				ContinuedExecutionRunId: nil,
			},
		},
		&internal.HistoryEvent{
			EventId:   common.Int64Ptr(5),
			Version:   common.Int64Ptr(123),
			Timestamp: common.Int64Ptr(time.Now().UnixNano()),
			EventType: internal.EventTypeWorkflowExecutionContinuedAsNew.Ptr(),
			WorkflowExecutionContinuedAsNewEventAttributes: &internal.WorkflowExecutionContinuedAsNewEventAttributes{
				NewExecutionRunId: common.StringPtr(endingRunID),
			},
		},
	}
	midBlob2 := s.serializeEvents(midEventBatch2)

	endingEventBatch := []*internal.HistoryEvent{
		&internal.HistoryEvent{
			EventId:   common.Int64Ptr(1),
			Version:   common.Int64Ptr(123),
			Timestamp: common.Int64Ptr(time.Now().UnixNano()),
			EventType: internal.EventTypeWorkflowExecutionStarted.Ptr(),
			WorkflowExecutionStartedEventAttributes: &internal.WorkflowExecutionStartedEventAttributes{
				ContinuedExecutionRunId: common.StringPtr(midRunID2),
			},
		},
		&internal.HistoryEvent{
			EventId:   common.Int64Ptr(2),
			Version:   common.Int64Ptr(123),
			Timestamp: common.Int64Ptr(time.Now().UnixNano()),
			EventType: internal.EventTypeDecisionTaskScheduled.Ptr(),
		},
	}
	endingBlob := s.serializeEvents(endingEventBatch)

	s.mockFrontendClient.On("GetWorkflowExecutionRawHistory", mock.Anything, &external.GetWorkflowExecutionRawHistoryRequest{
		Domain: common.StringPtr(s.domainName),
		Execution: &external.WorkflowExecution{
			WorkflowId: common.StringPtr(workflowID),
			RunId:      common.StringPtr(beginingRunID),
		},
		BranchToken:     nil,
		FirstEventId:    common.Int64Ptr(beginingEventID),
		NextEventId:     common.Int64Ptr(common.EndEventID),
		MaximumPageSize: common.Int32Ptr(pageSize),
		NextPageToken:   nil,
	}).Return(&external.GetWorkflowExecutionRawHistoryResponse{
		BranchToken:       branchToken,
		HistoryBatches:    []*external.DataBlob{beginingBlob},
		NextPageToken:     nil,
		ReplicationInfo:   beginingReplicationInfo,
		EventStoreVersion: common.Int32Ptr(beginingEventStoreVersion),
	}, nil).Once()

	s.mockFrontendClient.On("GetWorkflowExecutionRawHistory", mock.Anything, &external.GetWorkflowExecutionRawHistoryRequest{
		Domain: common.StringPtr(s.domainName),
		Execution: &external.WorkflowExecution{
			WorkflowId: common.StringPtr(workflowID),
			RunId:      common.StringPtr(midRunID1),
		},
		BranchToken:     nil,
		FirstEventId:    common.Int64Ptr(common.FirstEventID),
		NextEventId:     common.Int64Ptr(common.EndEventID),
		MaximumPageSize: common.Int32Ptr(1),
		NextPageToken:   nil,
	}).Return(&external.GetWorkflowExecutionRawHistoryResponse{
		BranchToken:       branchToken,
		HistoryBatches:    []*external.DataBlob{midBlob1},
		NextPageToken:     nil,
		ReplicationInfo:   midReplicationInfo1,
		EventStoreVersion: common.Int32Ptr(midEventStoreVersion1),
	}, nil).Once()

	s.mockFrontendClient.On("GetWorkflowExecutionRawHistory", mock.Anything, &external.GetWorkflowExecutionRawHistoryRequest{
		Domain: common.StringPtr(s.domainName),
		Execution: &external.WorkflowExecution{
			WorkflowId: common.StringPtr(workflowID),
			RunId:      common.StringPtr(midRunID1),
		},
		BranchToken:     nil,
		FirstEventId:    common.Int64Ptr(common.FirstEventID),
		NextEventId:     common.Int64Ptr(common.EndEventID),
		MaximumPageSize: common.Int32Ptr(pageSize),
		NextPageToken:   nil,
	}).Return(&external.GetWorkflowExecutionRawHistoryResponse{
		BranchToken:       branchToken,
		HistoryBatches:    []*external.DataBlob{midBlob1},
		NextPageToken:     nil,
		ReplicationInfo:   midReplicationInfo1,
		EventStoreVersion: common.Int32Ptr(midEventStoreVersion1),
	}, nil).Once()

	s.mockFrontendClient.On("GetWorkflowExecutionRawHistory", mock.Anything, &external.GetWorkflowExecutionRawHistoryRequest{
		Domain: common.StringPtr(s.domainName),
		Execution: &external.WorkflowExecution{
			WorkflowId: common.StringPtr(workflowID),
			RunId:      common.StringPtr(endingRunID),
		},
		BranchToken:     nil,
		FirstEventId:    common.Int64Ptr(common.FirstEventID),
		NextEventId:     common.Int64Ptr(common.EndEventID),
		MaximumPageSize: common.Int32Ptr(1),
		NextPageToken:   nil,
	}).Return(&external.GetWorkflowExecutionRawHistoryResponse{
		BranchToken:       branchToken,
		HistoryBatches:    []*external.DataBlob{endingBlob},
		NextPageToken:     nil,
		ReplicationInfo:   endingReplicationInfo,
		EventStoreVersion: common.Int32Ptr(endingEventStoreVersion),
	}, nil).Once()

	s.mockFrontendClient.On("GetWorkflowExecutionRawHistory", mock.Anything, &external.GetWorkflowExecutionRawHistoryRequest{
		Domain: common.StringPtr(s.domainName),
		Execution: &external.WorkflowExecution{
			WorkflowId: common.StringPtr(workflowID),
			RunId:      common.StringPtr(midRunID2),
		},
		BranchToken:     nil,
		FirstEventId:    common.Int64Ptr(common.FirstEventID),
		NextEventId:     common.Int64Ptr(common.EndEventID),
		MaximumPageSize: common.Int32Ptr(1),
		NextPageToken:   nil,
	}).Return(&external.GetWorkflowExecutionRawHistoryResponse{
		BranchToken:       branchToken,
		HistoryBatches:    []*external.DataBlob{midBlob2},
		NextPageToken:     nil,
		ReplicationInfo:   midReplicationInfo2,
		EventStoreVersion: common.Int32Ptr(midEventStoreVersion2),
	}, nil).Once()

	s.mockFrontendClient.On("GetWorkflowExecutionRawHistory", mock.Anything, &external.GetWorkflowExecutionRawHistoryRequest{
		Domain: common.StringPtr(s.domainName),
		Execution: &external.WorkflowExecution{
			WorkflowId: common.StringPtr(workflowID),
			RunId:      common.StringPtr(midRunID2),
		},
		BranchToken:     nil,
		FirstEventId:    common.Int64Ptr(common.FirstEventID),
		NextEventId:     common.Int64Ptr(common.EndEventID),
		MaximumPageSize: common.Int32Ptr(pageSize),
		NextPageToken:   nil,
	}).Return(&external.GetWorkflowExecutionRawHistoryResponse{
		BranchToken:       branchToken,
		HistoryBatches:    []*external.DataBlob{midBlob2},
		NextPageToken:     nil,
		ReplicationInfo:   midReplicationInfo2,
		EventStoreVersion: common.Int32Ptr(midEventStoreVersion2),
	}, nil).Once()

	s.mockFrontendClient.On("GetWorkflowExecutionRawHistory", mock.Anything, &external.GetWorkflowExecutionRawHistoryRequest{
		Domain: common.StringPtr(s.domainName),
		Execution: &external.WorkflowExecution{
			WorkflowId: common.StringPtr(workflowID),
			RunId:      common.StringPtr(endingRunID),
		},
		BranchToken:     nil,
		FirstEventId:    common.Int64Ptr(common.FirstEventID),
		NextEventId:     common.Int64Ptr(common.EndEventID),
		MaximumPageSize: common.Int32Ptr(1),
		NextPageToken:   nil,
	}).Return(&external.GetWorkflowExecutionRawHistoryResponse{
		BranchToken:       branchToken,
		HistoryBatches:    []*external.DataBlob{endingBlob},
		NextPageToken:     nil,
		ReplicationInfo:   endingReplicationInfo,
		EventStoreVersion: common.Int32Ptr(endingEventStoreVersion),
	}, nil).Once()

	s.mockFrontendClient.On("GetWorkflowExecutionRawHistory", mock.Anything, &external.GetWorkflowExecutionRawHistoryRequest{
		Domain: common.StringPtr(s.domainName),
		Execution: &external.WorkflowExecution{
			WorkflowId: common.StringPtr(workflowID),
			RunId:      common.StringPtr(endingRunID),
		},
		BranchToken:     nil,
		FirstEventId:    common.Int64Ptr(common.FirstEventID),
		NextEventId:     common.Int64Ptr(endingEventID),
		MaximumPageSize: common.Int32Ptr(pageSize),
		NextPageToken:   nil,
	}).Return(&external.GetWorkflowExecutionRawHistoryResponse{
		BranchToken:       branchToken,
		HistoryBatches:    []*external.DataBlob{endingBlob},
		NextPageToken:     nil,
		ReplicationInfo:   endingReplicationInfo,
		EventStoreVersion: common.Int32Ptr(endingEventStoreVersion),
	}, nil).Once()

	// ReplicateRawEvents is already tested, just count how many times this is called
	s.mockHistoryClient.On("ReplicateRawEvents", mock.Anything, mock.Anything).Return(nil).Times(4)

	err := s.rereplicator.SendMultiWorkflowHistory(s.domainID, workflowID,
		beginingRunID, beginingEventID, endingRunID, endingEventID)
	s.Nil(err)
}

func (s *historyRereplicatorSuite) TestSendSingleWorkflowHistory_NotContinueAsNew() {
	workflowID := "some random workflow ID"
	runID := uuid.New()
	branchToken := []byte("some random branch token")
	nextToken := []byte("some random next token")
	pageSize := int32(100)
	replicationInfo := map[string]*external.ReplicationInfo{
		"random data center": &external.ReplicationInfo{
			Version:     common.Int64Ptr(777),
			LastEventId: common.Int64Ptr(999),
		},
	}
	eventStoreVersion := int32(9)

	eventBatch1 := []*internal.HistoryEvent{
		&internal.HistoryEvent{
			EventId:   common.Int64Ptr(1),
			Version:   common.Int64Ptr(123),
			Timestamp: common.Int64Ptr(time.Now().UnixNano()),
			EventType: internal.EventTypeWorkflowExecutionStarted.Ptr(),
		},
		&internal.HistoryEvent{
			EventId:   common.Int64Ptr(2),
			Version:   common.Int64Ptr(123),
			Timestamp: common.Int64Ptr(time.Now().UnixNano()),
			EventType: internal.EventTypeDecisionTaskScheduled.Ptr(),
		},
		&internal.HistoryEvent{
			EventId:   common.Int64Ptr(3),
			Version:   common.Int64Ptr(123),
			Timestamp: common.Int64Ptr(time.Now().UnixNano()),
			EventType: internal.EventTypeDecisionTaskStarted.Ptr(),
		},
	}
	blob1 := s.serializeEvents(eventBatch1)

	eventBatch2 := []*internal.HistoryEvent{
		&internal.HistoryEvent{
			EventId:   common.Int64Ptr(4),
			Version:   common.Int64Ptr(123),
			Timestamp: common.Int64Ptr(time.Now().UnixNano()),
			EventType: internal.EventTypeDecisionTaskCompleted.Ptr(),
		},
		&internal.HistoryEvent{
			EventId:   common.Int64Ptr(5),
			Version:   common.Int64Ptr(123),
			Timestamp: common.Int64Ptr(time.Now().UnixNano()),
			EventType: internal.EventTypeWorkflowExecutionCompleted.Ptr(),
		},
	}
	blob2 := s.serializeEvents(eventBatch2)

	s.mockFrontendClient.On("GetWorkflowExecutionRawHistory", mock.Anything, &external.GetWorkflowExecutionRawHistoryRequest{
		Domain: common.StringPtr(s.domainName),
		Execution: &external.WorkflowExecution{
			WorkflowId: common.StringPtr(workflowID),
			RunId:      common.StringPtr(runID),
		},
		BranchToken:     nil,
		FirstEventId:    common.Int64Ptr(common.FirstEventID),
		NextEventId:     common.Int64Ptr(common.EndEventID),
		MaximumPageSize: common.Int32Ptr(pageSize),
		NextPageToken:   nil,
	}).Return(&external.GetWorkflowExecutionRawHistoryResponse{
		BranchToken:       branchToken,
		HistoryBatches:    []*external.DataBlob{blob1},
		NextPageToken:     nextToken,
		ReplicationInfo:   replicationInfo,
		EventStoreVersion: common.Int32Ptr(eventStoreVersion),
	}, nil).Once()

	s.mockFrontendClient.On("GetWorkflowExecutionRawHistory", mock.Anything, &external.GetWorkflowExecutionRawHistoryRequest{
		Domain: common.StringPtr(s.domainName),
		Execution: &external.WorkflowExecution{
			WorkflowId: common.StringPtr(workflowID),
			RunId:      common.StringPtr(runID),
		},
		BranchToken:     branchToken,
		FirstEventId:    common.Int64Ptr(common.FirstEventID),
		NextEventId:     common.Int64Ptr(common.EndEventID),
		MaximumPageSize: common.Int32Ptr(pageSize),
		NextPageToken:   nextToken,
	}).Return(&external.GetWorkflowExecutionRawHistoryResponse{
		BranchToken:       branchToken,
		HistoryBatches:    []*external.DataBlob{blob2},
		NextPageToken:     nil,
		ReplicationInfo:   replicationInfo,
		EventStoreVersion: common.Int32Ptr(eventStoreVersion),
	}, nil).Once()

	s.mockHistoryClient.On("ReplicateRawEvents", mock.Anything, &history.ReplicateRawEventsRequest{
		DomainUUID: common.StringPtr(s.domainID),
		WorkflowExecution: &internal.WorkflowExecution{
			WorkflowId: common.StringPtr(workflowID),
			RunId:      common.StringPtr(runID),
		},
		ReplicationInfo: s.rereplicator.replicationInfoFromPublic(replicationInfo),
		History: &internal.DataBlob{
			EncodingType: internal.EncodingTypeThriftRW.Ptr(),
			Data:         blob1.Data,
		},
		NewRunHistory:           nil,
		EventStoreVersion:       common.Int32Ptr(eventStoreVersion),
		NewRunEventStoreVersion: nil,
	}).Return(nil).Once()

	s.mockHistoryClient.On("ReplicateRawEvents", mock.Anything, &history.ReplicateRawEventsRequest{
		DomainUUID: common.StringPtr(s.domainID),
		WorkflowExecution: &internal.WorkflowExecution{
			WorkflowId: common.StringPtr(workflowID),
			RunId:      common.StringPtr(runID),
		},
		ReplicationInfo: s.rereplicator.replicationInfoFromPublic(replicationInfo),
		History: &internal.DataBlob{
			EncodingType: internal.EncodingTypeThriftRW.Ptr(),
			Data:         blob2.Data,
		},
		NewRunHistory:           nil,
		EventStoreVersion:       common.Int32Ptr(eventStoreVersion),
		NewRunEventStoreVersion: nil,
	}).Return(nil).Once()

	nextRunID, err := s.rereplicator.sendSingleWorkflowHistory(s.domainID, workflowID, runID, common.FirstEventID, common.EndEventID)
	s.Nil(err)
	s.Equal("", nextRunID)
}

func (s *historyRereplicatorSuite) TestSendSingleWorkflowHistory_ContinueAsNew() {
	workflowID := "some random workflow ID"
	runID := uuid.New()
	newRunID := uuid.New()
	branchToken := []byte("some random branch token")
	nextToken := []byte("some random next token")
	pageSize := int32(100)
	replicationInfo := map[string]*external.ReplicationInfo{
		"random data center": &external.ReplicationInfo{
			Version:     common.Int64Ptr(777),
			LastEventId: common.Int64Ptr(999),
		},
	}
	eventStoreVersion := int32(9)
	branchTokenNew := []byte("some random branch token for new run")
	replicationInfoNew := map[string]*external.ReplicationInfo{
		"random data center": &external.ReplicationInfo{
			Version:     common.Int64Ptr(222),
			LastEventId: common.Int64Ptr(111),
		},
	}
	eventStoreVersionNew := int32(88)

	eventBatch1 := []*internal.HistoryEvent{
		&internal.HistoryEvent{
			EventId:   common.Int64Ptr(1),
			Version:   common.Int64Ptr(123),
			Timestamp: common.Int64Ptr(time.Now().UnixNano()),
			EventType: internal.EventTypeWorkflowExecutionStarted.Ptr(),
		},
		&internal.HistoryEvent{
			EventId:   common.Int64Ptr(2),
			Version:   common.Int64Ptr(123),
			Timestamp: common.Int64Ptr(time.Now().UnixNano()),
			EventType: internal.EventTypeDecisionTaskScheduled.Ptr(),
		},
		&internal.HistoryEvent{
			EventId:   common.Int64Ptr(3),
			Version:   common.Int64Ptr(123),
			Timestamp: common.Int64Ptr(time.Now().UnixNano()),
			EventType: internal.EventTypeDecisionTaskStarted.Ptr(),
		},
	}
	blob1 := s.serializeEvents(eventBatch1)

	eventBatch2 := []*internal.HistoryEvent{
		&internal.HistoryEvent{
			EventId:   common.Int64Ptr(4),
			Version:   common.Int64Ptr(123),
			Timestamp: common.Int64Ptr(time.Now().UnixNano()),
			EventType: internal.EventTypeDecisionTaskCompleted.Ptr(),
		},
		&internal.HistoryEvent{
			EventId:   common.Int64Ptr(5),
			Version:   common.Int64Ptr(123),
			Timestamp: common.Int64Ptr(time.Now().UnixNano()),
			EventType: internal.EventTypeWorkflowExecutionContinuedAsNew.Ptr(),
			WorkflowExecutionContinuedAsNewEventAttributes: &internal.WorkflowExecutionContinuedAsNewEventAttributes{
				NewExecutionRunId: common.StringPtr(newRunID),
			},
		},
	}
	blob2 := s.serializeEvents(eventBatch2)

	eventBatchNew := []*internal.HistoryEvent{
		&internal.HistoryEvent{
			EventId:   common.Int64Ptr(1),
			Version:   common.Int64Ptr(223),
			Timestamp: common.Int64Ptr(time.Now().UnixNano()),
			EventType: internal.EventTypeWorkflowExecutionStarted.Ptr(),
			WorkflowExecutionStartedEventAttributes: &internal.WorkflowExecutionStartedEventAttributes{
				ContinuedExecutionRunId: common.StringPtr(runID),
			},
		},
	}
	blobNew := s.serializeEvents(eventBatchNew)

	s.mockFrontendClient.On("GetWorkflowExecutionRawHistory", mock.Anything, &external.GetWorkflowExecutionRawHistoryRequest{
		Domain: common.StringPtr(s.domainName),
		Execution: &external.WorkflowExecution{
			WorkflowId: common.StringPtr(workflowID),
			RunId:      common.StringPtr(runID),
		},
		BranchToken:     nil,
		FirstEventId:    common.Int64Ptr(common.FirstEventID),
		NextEventId:     common.Int64Ptr(common.EndEventID),
		MaximumPageSize: common.Int32Ptr(pageSize),
		NextPageToken:   nil,
	}).Return(&external.GetWorkflowExecutionRawHistoryResponse{
		BranchToken:       branchToken,
		HistoryBatches:    []*external.DataBlob{blob1},
		NextPageToken:     nextToken,
		ReplicationInfo:   replicationInfo,
		EventStoreVersion: common.Int32Ptr(eventStoreVersion),
	}, nil).Once()

	s.mockFrontendClient.On("GetWorkflowExecutionRawHistory", mock.Anything, &external.GetWorkflowExecutionRawHistoryRequest{
		Domain: common.StringPtr(s.domainName),
		Execution: &external.WorkflowExecution{
			WorkflowId: common.StringPtr(workflowID),
			RunId:      common.StringPtr(runID),
		},
		BranchToken:     branchToken,
		FirstEventId:    common.Int64Ptr(common.FirstEventID),
		NextEventId:     common.Int64Ptr(common.EndEventID),
		MaximumPageSize: common.Int32Ptr(pageSize),
		NextPageToken:   nextToken,
	}).Return(&external.GetWorkflowExecutionRawHistoryResponse{
		BranchToken:       branchToken,
		HistoryBatches:    []*external.DataBlob{blob2},
		NextPageToken:     nil,
		ReplicationInfo:   replicationInfo,
		EventStoreVersion: common.Int32Ptr(eventStoreVersion),
	}, nil).Once()

	s.mockFrontendClient.On("GetWorkflowExecutionRawHistory", mock.Anything, &external.GetWorkflowExecutionRawHistoryRequest{
		Domain: common.StringPtr(s.domainName),
		Execution: &external.WorkflowExecution{
			WorkflowId: common.StringPtr(workflowID),
			RunId:      common.StringPtr(newRunID),
		},
		BranchToken:     nil,
		FirstEventId:    common.Int64Ptr(common.FirstEventID),
		NextEventId:     common.Int64Ptr(common.EndEventID),
		MaximumPageSize: common.Int32Ptr(1),
		NextPageToken:   nil,
	}).Return(&external.GetWorkflowExecutionRawHistoryResponse{
		BranchToken:       branchTokenNew,
		HistoryBatches:    []*external.DataBlob{blobNew},
		NextPageToken:     nil,
		ReplicationInfo:   replicationInfoNew,
		EventStoreVersion: common.Int32Ptr(eventStoreVersionNew),
	}, nil).Once()

	s.mockHistoryClient.On("ReplicateRawEvents", mock.Anything, &history.ReplicateRawEventsRequest{
		DomainUUID: common.StringPtr(s.domainID),
		WorkflowExecution: &internal.WorkflowExecution{
			WorkflowId: common.StringPtr(workflowID),
			RunId:      common.StringPtr(runID),
		},
		ReplicationInfo: s.rereplicator.replicationInfoFromPublic(replicationInfo),
		History: &internal.DataBlob{
			EncodingType: internal.EncodingTypeThriftRW.Ptr(),
			Data:         blob1.Data,
		},
		NewRunHistory:           nil,
		EventStoreVersion:       common.Int32Ptr(eventStoreVersion),
		NewRunEventStoreVersion: nil,
	}).Return(nil).Once()

	s.mockHistoryClient.On("ReplicateRawEvents", mock.Anything, &history.ReplicateRawEventsRequest{
		DomainUUID: common.StringPtr(s.domainID),
		WorkflowExecution: &internal.WorkflowExecution{
			WorkflowId: common.StringPtr(workflowID),
			RunId:      common.StringPtr(runID),
		},
		ReplicationInfo: s.rereplicator.replicationInfoFromPublic(replicationInfo),
		History: &internal.DataBlob{
			EncodingType: internal.EncodingTypeThriftRW.Ptr(),
			Data:         blob2.Data,
		},
		NewRunHistory: &internal.DataBlob{
			EncodingType: internal.EncodingTypeThriftRW.Ptr(),
			Data:         blobNew.Data,
		},
		EventStoreVersion:       common.Int32Ptr(eventStoreVersion),
		NewRunEventStoreVersion: common.Int32Ptr(eventStoreVersionNew),
	}).Return(nil).Once()

	nextRunID, err := s.rereplicator.sendSingleWorkflowHistory(s.domainID, workflowID, runID, common.FirstEventID, common.EndEventID)
	s.Nil(err)
	s.Equal(newRunID, nextRunID)
}

func (s *historyRereplicatorSuite) TestEventIDRange() {
	// test case where begining run ID != ending run ID
	beginingRunID := "00001111-2222-3333-4444-555566667777"
	beginingEventID := int64(144)
	endingRunID := "00001111-2222-3333-4444-555566668888"
	endingEventID := int64(1)

	runID := beginingRunID
	firstEventID, nextEventID := s.rereplicator.eventIDRange(runID, beginingRunID, beginingEventID, endingRunID, endingEventID)
	s.Equal(beginingEventID, firstEventID)
	s.Equal(common.EndEventID, nextEventID)

	runID = uuid.New()
	firstEventID, nextEventID = s.rereplicator.eventIDRange(runID, beginingRunID, beginingEventID, endingRunID, endingEventID)
	s.Equal(common.FirstEventID, firstEventID)
	s.Equal(common.EndEventID, nextEventID)

	runID = endingRunID
	firstEventID, nextEventID = s.rereplicator.eventIDRange(runID, beginingRunID, beginingEventID, endingRunID, endingEventID)
	s.Equal(common.FirstEventID, firstEventID)
	s.Equal(endingEventID, nextEventID)

	// test case where begining run ID != ending run ID
	beginingRunID = "00001111-2222-3333-4444-555566667777"
	beginingEventID = int64(144)
	endingRunID = beginingRunID
	endingEventID = endingEventID + 100
	runID = beginingRunID
	firstEventID, nextEventID = s.rereplicator.eventIDRange(runID, beginingRunID, beginingEventID, endingRunID, endingEventID)
	s.Equal(beginingEventID, firstEventID)
	s.Equal(endingEventID, nextEventID)
}

func (s *historyRereplicatorSuite) TestCreateReplicationRawRequest() {
	workflowID := "some random workflow ID"
	runID := uuid.New()
	blob := &internal.DataBlob{
		EncodingType: internal.EncodingTypeThriftRW.Ptr(),
		Data:         []byte("some random history blob"),
	}
	eventStoreVersion := int32(55)
	replicationInfo := map[string]*internal.ReplicationInfo{
		"random data center": &internal.ReplicationInfo{
			Version:     common.Int64Ptr(777),
			LastEventId: common.Int64Ptr(999),
		},
	}

	s.Equal(&history.ReplicateRawEventsRequest{
		DomainUUID: common.StringPtr(s.domainID),
		WorkflowExecution: &internal.WorkflowExecution{
			WorkflowId: common.StringPtr(workflowID),
			RunId:      common.StringPtr(runID),
		},
		ReplicationInfo:         replicationInfo,
		History:                 blob,
		EventStoreVersion:       common.Int32Ptr(eventStoreVersion),
		NewRunHistory:           nil,
		NewRunEventStoreVersion: nil,
	}, s.rereplicator.createReplicationRawRequest(s.domainID, workflowID, runID, blob, eventStoreVersion, replicationInfo))
}

func (s *historyRereplicatorSuite) TestSendReplicationRawRequest() {
	// test that nil request will be a no op
	s.Nil(s.rereplicator.sendReplicationRawRequest(nil))

	request := &history.ReplicateRawEventsRequest{
		DomainUUID: common.StringPtr(s.domainID),
		WorkflowExecution: &internal.WorkflowExecution{
			WorkflowId: common.StringPtr("some random workflow ID"),
			RunId:      common.StringPtr(uuid.New()),
		},
		ReplicationInfo: map[string]*internal.ReplicationInfo{
			"random data center": &internal.ReplicationInfo{
				Version:     common.Int64Ptr(777),
				LastEventId: common.Int64Ptr(999),
			},
		},
		History: &internal.DataBlob{
			EncodingType: internal.EncodingTypeThriftRW.Ptr(),
			Data:         []byte("some random history blob"),
		},
		NewRunHistory: &internal.DataBlob{
			EncodingType: internal.EncodingTypeThriftRW.Ptr(),
			Data:         []byte("some random new run history blob"),
		},
		EventStoreVersion:       common.Int32Ptr(0),
		NewRunEventStoreVersion: common.Int32Ptr(2),
	}

	s.mockHistoryClient.On("ReplicateRawEvents", mock.Anything, request).Return(nil).Once()
	err := s.rereplicator.sendReplicationRawRequest(request)
	s.Nil(err)
}

func (s *historyRereplicatorSuite) TestGetHistory() {
	workflowID := "some random workflow ID"
	runID := uuid.New()
	branchToken := []byte("some random branch token")
	firstEventID := int64(123)
	nextEventID := int64(345)
	nextTokenIn := []byte("some random next token in")
	nextTokenOut := []byte("some random next token out")
	pageSize := int32(59)
	blob := []byte("some random events blob")

	response := &external.GetWorkflowExecutionRawHistoryResponse{
		BranchToken: branchToken,
		HistoryBatches: []*external.DataBlob{&external.DataBlob{
			EncodingType: external.EncodingTypeThriftRW.Ptr(),
			Data:         blob,
		}},
		NextPageToken: nextTokenOut,
		ReplicationInfo: map[string]*external.ReplicationInfo{
			"random data center": &external.ReplicationInfo{
				Version:     common.Int64Ptr(777),
				LastEventId: common.Int64Ptr(999),
			},
		},
		EventStoreVersion: common.Int32Ptr(22),
	}
	s.mockFrontendClient.On("GetWorkflowExecutionRawHistory", mock.Anything, &external.GetWorkflowExecutionRawHistoryRequest{
		Domain: common.StringPtr(s.domainName),
		Execution: &external.WorkflowExecution{
			WorkflowId: common.StringPtr(workflowID),
			RunId:      common.StringPtr(runID),
		},
		BranchToken:     branchToken,
		FirstEventId:    common.Int64Ptr(firstEventID),
		NextEventId:     common.Int64Ptr(nextEventID),
		MaximumPageSize: common.Int32Ptr(pageSize),
		NextPageToken:   nextTokenIn,
	}).Return(response, nil).Once()

	out, err := s.rereplicator.getHistory(s.domainID, workflowID, runID, branchToken, firstEventID, nextEventID, nextTokenIn, pageSize)
	s.Nil(err)
	s.Equal(response, out)
}

func (s *historyRereplicatorSuite) TestGetPrevEventID() {
	workflowID := "some random workflow ID"
	currentRunID := uuid.New()

	prepareFn := func(prevRunID *string) {
		eventBatch := []*internal.HistoryEvent{
			&internal.HistoryEvent{
				EventId:   common.Int64Ptr(1),
				Version:   common.Int64Ptr(123),
				Timestamp: common.Int64Ptr(time.Now().UnixNano()),
				EventType: internal.EventTypeWorkflowExecutionStarted.Ptr(),
				WorkflowExecutionStartedEventAttributes: &internal.WorkflowExecutionStartedEventAttributes{
					ContinuedExecutionRunId: prevRunID,
				},
			},
			&internal.HistoryEvent{
				EventId:   common.Int64Ptr(2),
				Version:   common.Int64Ptr(223),
				Timestamp: common.Int64Ptr(time.Now().UnixNano()),
				EventType: internal.EventTypeDecisionTaskScheduled.Ptr(),
			},
		}
		blob, err := s.serializer.SerializeBatchEvents(eventBatch, common.EncodingTypeThriftRW)
		s.Nil(err)

		s.mockFrontendClient.On("GetWorkflowExecutionRawHistory", mock.Anything, &external.GetWorkflowExecutionRawHistoryRequest{
			Domain: common.StringPtr(s.domainName),
			Execution: &external.WorkflowExecution{
				WorkflowId: common.StringPtr(workflowID),
				RunId:      common.StringPtr(currentRunID),
			},
			BranchToken:     nil,
			FirstEventId:    common.Int64Ptr(common.FirstEventID),
			NextEventId:     common.Int64Ptr(common.EndEventID),
			MaximumPageSize: common.Int32Ptr(1),
			NextPageToken:   nil,
		}).Return(&external.GetWorkflowExecutionRawHistoryResponse{
			HistoryBatches: []*external.DataBlob{&external.DataBlob{
				EncodingType: external.EncodingTypeThriftRW.Ptr(),
				Data:         blob.Data,
			}},
		}, nil).Once()
	}

	// has prev run
	prevRunID := uuid.New()
	prepareFn(common.StringPtr(prevRunID))
	runID, err := s.rereplicator.getPrevRunID(s.domainID, workflowID, currentRunID)
	s.Nil(err)
	s.Equal(prevRunID, runID)

	// no prev run
	prepareFn(nil)
	runID, err = s.rereplicator.getPrevRunID(s.domainID, workflowID, currentRunID)
	s.Nil(err)
	s.Equal("", runID)
}

func (s *historyRereplicatorSuite) TestGetNextRunID_ContinueAsNew() {
	nextRunID := uuid.New()
	eventBatchIn := []*internal.HistoryEvent{
		&internal.HistoryEvent{
			EventId:   common.Int64Ptr(233),
			Version:   common.Int64Ptr(123),
			Timestamp: common.Int64Ptr(time.Now().UnixNano()),
			EventType: internal.EventTypeDecisionTaskCompleted.Ptr(),
		},
		&internal.HistoryEvent{
			EventId:   common.Int64Ptr(234),
			Version:   common.Int64Ptr(223),
			Timestamp: common.Int64Ptr(time.Now().UnixNano()),
			EventType: internal.EventTypeWorkflowExecutionContinuedAsNew.Ptr(),
			WorkflowExecutionContinuedAsNewEventAttributes: &internal.WorkflowExecutionContinuedAsNewEventAttributes{
				NewExecutionRunId: common.StringPtr(nextRunID),
			},
		},
	}
	blob, err := s.serializer.SerializeBatchEvents(eventBatchIn, common.EncodingTypeThriftRW)
	s.Nil(err)

	runID, err := s.rereplicator.getNextRunID(&internal.DataBlob{
		EncodingType: internal.EncodingTypeThriftRW.Ptr(),
		Data:         blob.Data,
	})
	s.Nil(err)
	s.Equal(nextRunID, runID)
}

func (s *historyRereplicatorSuite) TestGetNextRunID_NotContinueAsNew() {
	eventBatchIn := []*internal.HistoryEvent{
		&internal.HistoryEvent{
			EventId:   common.Int64Ptr(233),
			Version:   common.Int64Ptr(123),
			Timestamp: common.Int64Ptr(time.Now().UnixNano()),
			EventType: internal.EventTypeDecisionTaskCompleted.Ptr(),
		},
		&internal.HistoryEvent{
			EventId:   common.Int64Ptr(234),
			Version:   common.Int64Ptr(223),
			Timestamp: common.Int64Ptr(time.Now().UnixNano()),
			EventType: internal.EventTypeWorkflowExecutionCanceled.Ptr(),
			WorkflowExecutionCancelRequestedEventAttributes: &internal.WorkflowExecutionCancelRequestedEventAttributes{},
		},
	}
	blob, err := s.serializer.SerializeBatchEvents(eventBatchIn, common.EncodingTypeThriftRW)
	s.Nil(err)

	runID, err := s.rereplicator.getNextRunID(&internal.DataBlob{
		EncodingType: internal.EncodingTypeThriftRW.Ptr(),
		Data:         blob.Data,
	})
	s.Nil(err)
	s.Equal("", runID)
}

func (s *historyRereplicatorSuite) TestDeserializeBlob() {
	eventBatchIn := []*internal.HistoryEvent{
		&internal.HistoryEvent{
			EventId:   common.Int64Ptr(1),
			Version:   common.Int64Ptr(123),
			Timestamp: common.Int64Ptr(time.Now().UnixNano()),
			EventType: internal.EventTypeWorkflowExecutionStarted.Ptr(),
		},
		&internal.HistoryEvent{
			EventId:   common.Int64Ptr(2),
			Version:   common.Int64Ptr(223),
			Timestamp: common.Int64Ptr(time.Now().UnixNano()),
			EventType: internal.EventTypeDecisionTaskScheduled.Ptr(),
		},
	}

	blob, err := s.serializer.SerializeBatchEvents(eventBatchIn, common.EncodingTypeThriftRW)
	s.Nil(err)

	eventBatchOut, err := s.rereplicator.deserializeBlob(&internal.DataBlob{
		EncodingType: internal.EncodingTypeThriftRW.Ptr(),
		Data:         blob.Data,
	})
	s.Nil(err)
	s.Equal(eventBatchIn, eventBatchOut)
}

func (s *historyRereplicatorSuite) TestDataBlobConversion() {
	data := []byte("some random data blob")

	internalBlob := &internal.DataBlob{
		EncodingType: internal.EncodingTypeThriftRW.Ptr(),
		Data:         data,
	}
	externalBlob := &external.DataBlob{
		EncodingType: external.EncodingTypeThriftRW.Ptr(),
		Data:         data,
	}

	blob1, err := s.rereplicator.dataBlobFromPublic(externalBlob)
	s.Nil(err)
	s.Equal(internalBlob, blob1)

	blob2, err := s.rereplicator.dataBlobToPublic(internalBlob)
	s.Nil(err)
	s.Equal(externalBlob, blob2)
}

func (s *historyRereplicatorSuite) TestReplicationInfoConversion() {
	cluster := "some random cluster"
	var version int64 = 144
	var lastEventID int64 = 2333

	internalRepliationInfo := map[string]*internal.ReplicationInfo{
		cluster: &internal.ReplicationInfo{
			Version:     common.Int64Ptr(version),
			LastEventId: common.Int64Ptr(lastEventID),
		},
	}
	externalRepliationInfo := map[string]*external.ReplicationInfo{
		cluster: &external.ReplicationInfo{
			Version:     common.Int64Ptr(version),
			LastEventId: common.Int64Ptr(lastEventID),
		},
	}
	s.Equal(internalRepliationInfo, s.rereplicator.replicationInfoFromPublic(externalRepliationInfo))
	s.Equal(externalRepliationInfo, s.rereplicator.replicationInfoToPublic(internalRepliationInfo))
}

func (s *historyRereplicatorSuite) serializeEvents(events []*internal.HistoryEvent) *external.DataBlob {
	blob, err := s.serializer.SerializeBatchEvents(events, common.EncodingTypeThriftRW)
	s.Nil(err)
	return &external.DataBlob{
		EncodingType: external.EncodingTypeThriftRW.Ptr(),
		Data:         blob.Data,
	}
}
