package types

import (
	"github.com/coneno/logger"
	api "github.com/influenzanet/messaging-service/pkg/api/messaging_service"
)

type Expression struct {
	Name       string          `bson:"name"`
	ReturnType string          `bson:"returnType,omitempty"`
	Data       []ExpressionArg `bson:"data,omitempty"`
}

type ExpressionArg struct {
	DType string      `bson:"dtype"`
	Exp   *Expression `bson:"exp,omitempty"`
	Str   string      `bson:"str,omitempty"`
	Num   float64     `bson:"num,omitempty"`
}

func (e *ExpressionArg) ToAPI() *api.ExpressionArg {
	if e == nil {
		return nil
	}
	eargs := &api.ExpressionArg{}
	if e.Exp != nil && len(e.Exp.Name) > 0 {
		eargs.Data = &api.ExpressionArg_Exp{Exp: e.Exp.ToAPI()}
	} else if len(e.Str) > 0 {
		eargs.Data = &api.ExpressionArg_Str{Str: e.Str}
	} else {
		eargs.Data = &api.ExpressionArg_Num{Num: e.Num}
	}
	eargs.Dtype = e.DType
	return eargs
}

func (e *Expression) ToAPI() *api.Expression {
	if e == nil {
		return nil
	}
	data := make([]*api.ExpressionArg, len(e.Data))
	for i, ea := range e.Data {
		data[i] = ea.ToAPI()
	}
	return &api.Expression{
		Name:       e.Name,
		ReturnType: e.ReturnType,
		Data:       data,
	}
}

func ExpressionArgFromAPI(e *api.ExpressionArg) *ExpressionArg {
	newEA := ExpressionArg{}
	if e == nil {
		return nil
	}

	switch x := e.Data.(type) {
	case *api.ExpressionArg_Exp:
		newEA.Exp = ExpressionFromAPI(x.Exp)
	case *api.ExpressionArg_Str:
		newEA.Str = x.Str
	case *api.ExpressionArg_Num:
		newEA.Num = x.Num
	case nil:
		// The field is not set.
	default:
		logger.Error.Printf("api.ExpressionArg has unexpected type %T", x)
	}
	newEA.DType = e.Dtype
	return &newEA
}

func ExpressionFromAPI(e *api.Expression) *Expression {
	exp := Expression{}
	if e == nil {
		return nil
	}
	exp.Name = e.Name
	exp.ReturnType = e.ReturnType

	exp.Data = make([]ExpressionArg, len(e.Data))
	for i, ea := range e.Data {
		exp.Data[i] = *ExpressionArgFromAPI(ea)
	}
	return &exp
}

func (exp ExpressionArg) IsExpression() bool {
	return exp.DType == "exp"
}
