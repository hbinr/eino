/*
 * Copyright 2024 CloudWeGo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package compose

import (
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/internal/mock/components/embedding"
	"github.com/cloudwego/eino/internal/mock/components/indexer"
	"github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestWorkflow(t *testing.T) {
	ctx := context.Background()

	type structA struct {
		Field1 string
		Field2 int
		Field3 []any
	}

	type structB struct {
		Field1 string
		Field2 int
	}

	type structC struct {
		Field1 string
	}

	type structE struct {
		Field1 string
		Field2 string
		Field3 []any
	}

	type structF struct {
		Field1    string
		Field2    string
		Field3    []any
		B         int
		StateTemp string
	}
	RegisterStreamChunkConcatFunc(func(ts []*structF) (*structF, error) {
		ret := &structF{}
		for _, tt := range ts {
			ret.Field1 += tt.Field1
			ret.Field2 += tt.Field2
			ret.Field3 = append(ret.Field3, tt.Field3...)
			ret.B += tt.B
			ret.StateTemp += tt.StateTemp
		}
		return ret, nil
	})

	type state struct {
		temp string
	}

	type structEnd struct {
		Field1 string
	}

	subGraph := NewGraph[string, *structB]()
	_ = subGraph.AddLambdaNode(
		"1",
		InvokableLambda(func(ctx context.Context, input string) (*structB, error) {
			return &structB{Field1: input, Field2: 33}, nil
		}),
	)
	_ = subGraph.AddEdge(START, "1")
	_ = subGraph.AddEdge("1", END)

	subChain := NewChain[any, *structC]().
		AppendLambda(InvokableLambda(func(_ context.Context, in any) (*structC, error) {
			return &structC{Field1: fmt.Sprintf("%d", in)}, nil
		}))

	type struct2 struct {
		F map[string]any
	}
	subWorkflow := NewWorkflow[[]any, []any]()
	subWorkflow.AddLambdaNode(
		"1",
		InvokableLambda(func(_ context.Context, in []any) ([]any, error) {
			return in, nil
		}),
		WithOutputKey("key")).
		AddInput(START) // []any -> map["key"][]any
	subWorkflow.AddLambdaNode(
		"2",
		InvokableLambda(func(_ context.Context, in []any) ([]any, error) {
			return in, nil
		}),
		WithInputKey("key"),
		WithOutputKey("key1")).
		AddInput("1") // map["key"][]any -> []any -> map["key1"][]any
	subWorkflow.AddLambdaNode(
		"3",
		InvokableLambda(func(_ context.Context, in struct2) (map[string]any, error) {
			return in.F, nil
		}),
	).
		AddInput("2", ToField("F")) // map["key1"][]any -> map["F"]map["key1"][]any -> struct2{F: map["key1"]any} -> map["key1"][]any
	subWorkflow.AddLambdaNode(
		"4",
		InvokableLambda(func(_ context.Context, in []any) ([]any, error) {
			return in, nil
		}),
		WithInputKey("key1"),
	).
		AddInput("3") // map["key1"][]any -> []any
	subWorkflow.AddEnd("4")

	w := NewWorkflow[*structA, *structEnd](WithGenLocalState(func(context.Context) *state { return &state{} }))

	w.
		AddGraphNode("B", subGraph,
			WithStatePostHandler(func(ctx context.Context, out *structB, state *state) (*structB, error) {
				state.temp = out.Field1
				return out, nil
			})).
		AddInput(START, FromField("Field1"))

	w.
		AddGraphNode("C", subChain).
		AddInput(START, FromField("Field2"))

	w.
		AddGraphNode("D", subWorkflow).
		AddInput(START, FromField("Field3"))

	w.
		AddLambdaNode(
			"E",
			TransformableLambda(func(_ context.Context, in *schema.StreamReader[structE]) (*schema.StreamReader[structE], error) {
				return schema.StreamReaderWithConvert(in, func(in structE) (structE, error) {
					if len(in.Field1) > 0 {
						in.Field1 = "E:" + in.Field1
					}
					if len(in.Field2) > 0 {
						in.Field2 = "E:" + in.Field2
					}

					return in, nil
				}), nil
			}),
			WithStreamStatePreHandler(func(ctx context.Context, in *schema.StreamReader[structE], state *state) (*schema.StreamReader[structE], error) {
				temp := state.temp
				return schema.StreamReaderWithConvert(in, func(v structE) (structE, error) {
					if len(v.Field3) > 0 {
						v.Field3 = append(v.Field3, "Pre:"+temp)
					}

					return v, nil
				}), nil
			}),
			WithStreamStatePostHandler(func(ctx context.Context, out *schema.StreamReader[structE], state *state) (*schema.StreamReader[structE], error) {
				return schema.StreamReaderWithConvert(out, func(v structE) (structE, error) {
					if len(v.Field1) > 0 {
						v.Field1 = v.Field1 + "+Post"
					}
					return v, nil
				}), nil
			})).
		AddInput("B", MapFields("Field1", "Field1")).
		AddInput("C", MapFields("Field1", "Field2")).
		AddInput("D", ToField("Field3"))

	w.
		AddLambdaNode(
			"F",
			InvokableLambda(func(ctx context.Context, in *structF) (string, error) {
				return fmt.Sprintf("%v_%v_%v_%v_%v", in.Field1, in.Field2, in.Field3, in.B, in.StateTemp), nil
			}),
			WithStatePreHandler(func(ctx context.Context, in *structF, state *state) (*structF, error) {
				in.StateTemp = state.temp
				return in, nil
			}),
		).
		AddInput("B", MapFields("Field2", "B")).
		AddInput("E",
			MapFields("Field1", "Field1"),
			MapFields("Field2", "Field2"),
			MapFields("Field3", "Field3"),
		)

	w.AddEnd("F", ToField("Field1"))

	compiled, err := w.Compile(ctx)
	assert.NoError(t, err)

	input := &structA{
		Field1: "1",
		Field2: 2,
		Field3: []any{
			1, "good",
		},
	}
	out, err := compiled.Invoke(ctx, input)
	assert.NoError(t, err)
	assert.Equal(t, &structEnd{"E:1+Post_E:2_[1 good Pre:1]_33_1"}, out)

	outStream, err := compiled.Stream(ctx, input)
	assert.NoError(t, err)
	defer outStream.Close()
	for {
		chunk, err := outStream.Recv()
		if err != nil {
			if err == io.EOF {
				break
			}

			t.Error(err)
			return
		}

		assert.Equal(t, &structEnd{"E:1+Post_E:2_[1 good Pre:1]_33_1"}, chunk)
	}
}

func TestWorkflowCompile(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	t.Run("compile without add end", func(t *testing.T) {
		w := NewWorkflow[*schema.Message, []*schema.Message]()
		w.AddToolsNode("1", &ToolsNode{}).AddInput(START)
		_, err := w.Compile(ctx)
		assert.ErrorContains(t, err, "end node not set")
	})

	t.Run("type mismatch", func(t *testing.T) {
		w := NewWorkflow[string, string]()
		w.AddToolsNode("1", &ToolsNode{}).AddInput(START)
		w.AddEnd("1")
		_, err := w.Compile(ctx)
		assert.ErrorContains(t, err, "mismatch")
	})

	t.Run("upstream not struct/struct ptr, mapping has FromField", func(t *testing.T) {
		w := NewWorkflow[[]*schema.Document, []string]()

		w.AddIndexerNode("indexer", indexer.NewMockIndexer(ctrl)).AddInput(START, FromField("F1"))
		w.AddEnd("indexer")
		_, err := w.Compile(ctx)
		assert.ErrorContains(t, err, "downstream output type should be struct")
	})

	t.Run("downstream not struct/struct ptr, mapping has ToField", func(t *testing.T) {
		w := NewWorkflow[[]string, [][]float64]()
		w.AddEmbeddingNode("embedder", embedding.NewMockEmbedder(ctrl)).AddInput(START, ToField("F1"))
		w.AddEnd("embedder")
		_, err := w.Compile(ctx)
		assert.ErrorContains(t, err, "upstream input type should be struct")
	})

	t.Run("map to non existing field in upstream", func(t *testing.T) {
		w := NewWorkflow[*schema.Message, []*schema.Message]()
		w.AddToolsNode("tools_node", &ToolsNode{}).AddInput(START, FromField("non_exist"))
		w.AddEnd("tools_node")
		_, err := w.Compile(ctx)
		assert.ErrorContains(t, err, "type[schema.Message] has no field[non_exist]")
	})

	t.Run("map to not exported field in downstream", func(t *testing.T) {
		w := NewWorkflow[string, *FieldMapping]()
		w.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
			return input, nil
		})).AddInput(START)
		w.AddEnd("1", ToField("to"))
		_, err := w.Compile(ctx)
		assert.ErrorContains(t, err, "type[compose.FieldMapping] has an unexported field[to]")
	})

	t.Run("duplicate node key", func(t *testing.T) {
		w := NewWorkflow[[]*schema.Message, []*schema.Message]()
		w.AddChatModelNode("1", model.NewMockChatModel(ctrl)).AddInput(START)
		w.AddToolsNode("1", &ToolsNode{}).AddInput("1")
		w.AddEnd("1")
		_, err := w.Compile(ctx)
		assert.ErrorContains(t, err, "node '1' already present")
	})

	t.Run("from non-existing node", func(t *testing.T) {
		w := NewWorkflow[*schema.Message, []*schema.Message]()
		w.AddToolsNode("1", &ToolsNode{}).AddInput(START)
		w.AddEnd("2")
		_, err := w.Compile(ctx)
		assert.ErrorContains(t, err, "edge start node '2' needs to be added to graph first")
	})
}

func TestFanInToSameDest(t *testing.T) {
	t.Run("traditional outputKey fan-in with map[string]any", func(t *testing.T) {
		wf := NewWorkflow[string, []*schema.Message]()
		wf.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, in string) (output string, err error) {
			return in, nil
		}), WithOutputKey("q1")).AddInput(START)
		wf.AddLambdaNode("2", InvokableLambda(func(ctx context.Context, in string) (output string, err error) {
			return in, nil
		}), WithOutputKey("q2")).AddInput(START)
		wf.AddChatTemplateNode("prompt", prompt.FromMessages(schema.Jinja2, schema.UserMessage("{{q1}}_{{q2}}"))).
			AddInput("1").AddInput("2")
		wf.AddEnd("prompt")
		c, err := wf.Compile(context.Background())
		assert.NoError(t, err)
		out, err := c.Invoke(context.Background(), "query")
		assert.NoError(t, err)
		assert.Equal(t, []*schema.Message{{Role: schema.User, Content: "query_query"}}, out)
	})

	t.Run("fan-in to a field of map", func(t *testing.T) {
		type dest struct {
			F map[string]any
		}

		type in struct {
			A string
			B int
		}

		wf := NewWorkflow[in, dest]()
		wf.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, in string) (output string, err error) {
			return in, nil
		}), WithOutputKey("A")).AddInput(START, FromField("A"))
		wf.AddLambdaNode("2", InvokableLambda(func(ctx context.Context, in int) (output int, err error) {
			return in, nil
		}), WithOutputKey("B")).AddInput(START, FromField("B"))
		wf.AddEnd("1", ToField("F")).AddEnd("2", ToField("F"))
		_, err := wf.Compile(context.Background())
		assert.ErrorContains(t, err, "duplicate mapping target field")
	})
}

type goodInterface interface {
	GOOD()
}
type goodStruct struct{}

func (g *goodStruct) GOOD() {}

func TestMayAssignableFieldMapping(t *testing.T) {
	type in struct {
		A goodInterface
	}
	wf := NewWorkflow[in, *goodStruct]()
	wf.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input *goodStruct) (output goodInterface, err error) { return input, nil })).
		AddInput(START, FromField("A"))
	wf.AddEnd("1")
	ctx := context.Background()
	r, err := wf.Compile(ctx)
	assert.NoError(t, err)
	result, err := r.Invoke(ctx, in{A: &goodStruct{}})
	assert.NoError(t, err)
	result.GOOD()
}
