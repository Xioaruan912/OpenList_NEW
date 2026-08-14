package common

import (
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/go-cache"
	"github.com/golang-jwt/jwt/v4"
	"github.com/pkg/errors"
)

var SecretKey []byte

type UserClaims struct {
	Username string `json:"username"`
	PwdTS    int64  `json:"pwd_ts"`
	jwt.RegisteredClaims
}

// invalidTokenCache 黑名单：仅记录主动失效（登出/改密）的 token，
// 不在黑名单的 token 只要 JWT 签名与有效期校验通过即有效，
// 因此服务重启后浏览器已登录状态保持，无需重新登录
var invalidTokenCache = cache.NewMemCache[bool]()

func GenerateToken(user *model.User) (tokenString string, err error) {
	claim := UserClaims{
		Username: user.Username,
		PwdTS:    user.PwdTS,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Duration(conf.Conf.TokenExpiresIn) * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now()),
		}}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claim)
	tokenString, err = token.SignedString(SecretKey)
	if err != nil {
		return "", err
	}
	return tokenString, err
}

func ParseToken(tokenString string) (*UserClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &UserClaims{}, func(token *jwt.Token) (interface{}, error) {
		return SecretKey, nil
	})
	if IsTokenInvalidated(tokenString) {
		return nil, errors.New("token is invalidated")
	}
	if err != nil {
		if ve, ok := err.(*jwt.ValidationError); ok {
			if ve.Errors&jwt.ValidationErrorMalformed != 0 {
				return nil, errors.New("that's not even a token")
			} else if ve.Errors&jwt.ValidationErrorExpired != 0 {
				return nil, errors.New("token is expired")
			} else if ve.Errors&jwt.ValidationErrorNotValidYet != 0 {
				return nil, errors.New("token not active yet")
			} else {
				return nil, errors.New("couldn't handle this token")
			}
		}
	}
	if claims, ok := token.Claims.(*UserClaims); ok && token.Valid {
		return claims, nil
	}
	return nil, errors.New("couldn't handle this token")
}

func InvalidateToken(tokenString string) error {
	if tokenString == "" {
		return nil // don't invalidate empty guest token
	}
	// 黑名单条目带 TTL（token 最大有效期），自动清理避免累积
	invalidTokenCache.Set(tokenString, true, cache.WithEx[bool](48*time.Hour))
	return nil
}

func IsTokenInvalidated(tokenString string) bool {
	_, ok := invalidTokenCache.Get(tokenString)
	return ok
}
