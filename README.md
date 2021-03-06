# MyStream API
MyStream은 JSON 형식의 REST API를 통해 데이터를 제공합니다.

## Getting Started
### 설치
GitHub의 [Release 페이지](https://github.com/awebow/MyStream-API/releases)에서 최신 릴리즈의 바이너리 혹은 소스 코드를 다운로드 받거나, 다음 명령어를 통해 저장소를 클론하여 최신 커밋의 소스 코드를 다운로드 받습니다.
```console
$ git clone https://github.com/awebow/MyStream-API
```

### 데이터베이스 셋업
MyStream은 MySQL 혹은 MariaDB를 사용합니다. 다음 SQL문을 실행하여 데이터베이스의 테이블을 생성합니다.

```sql
CREATE TABLE `users` (
  `id` char(26) CHARACTER NOT NULL,
  `email` varchar(255) CHARACTER NOT NULL,
  `password` binary(48) NOT NULL,
  `name` varchar(64) CHARACTER NOT NULL,
  `picture` varchar(255) CHARACTER DEFAULT NULL,
  `is_admin` tinyint(1) NOT NULL DEFAULT 0,
  `registered_at` datetime NOT NULL DEFAULT current_timestamp(),
  `deactivated_at` datetime DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `users_email_un` (`email`),
  KEY `users_registered_at_IDX` (`registered_at`) USING BTREE,
  KEY `users_deactivated_at_IDX` (`deactivated_at`) USING BTREE,
  KEY `users_is_admin_IDX` (`is_admin`) USING BTREE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE `channels` (
  `id` char(26) CHARACTER NOT NULL,
  `name` varchar(100) CHARACTER NOT NULL,
  `description` longtext CHARACTER DEFAULT NULL,
  `picture` varchar(255) CHARACTER DEFAULT NULL,
  `owner` char(26) CHARACTER NOT NULL,
  `subscribers` bigint(20) unsigned NOT NULL DEFAULT 0,
  `videos` bigint(20) unsigned NOT NULL DEFAULT 0,
  `created_at` datetime NOT NULL DEFAULT current_timestamp(),
  `updated_at` datetime NOT NULL DEFAULT current_timestamp(),
  `deactivated_at` datetime DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `channels_created_at_IDX` (`created_at`) USING BTREE,
  KEY `channels_deactivated_at_IDX` (`deactivated_at`) USING BTREE,
  KEY `channels_FK` (`owner`),
  KEY `channels_subscribers_IDX` (`subscribers`) USING BTREE,
  KEY `channels_videos_IDX` (`videos`) USING BTREE,
  CONSTRAINT `channels_FK` FOREIGN KEY (`owner`) REFERENCES `users` (`id`) ON DELETE CASCADE ON UPDATE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE `subscriptions` (
  `user_id` char(26) CHARACTER NOT NULL,
  `channel_id` char(26) CHARACTER NOT NULL,
  PRIMARY KEY (`user_id`,`channel_id`),
  KEY `subscriptions_channel_FK` (`channel_id`),
  CONSTRAINT `subscriptions_channel_FK` FOREIGN KEY (`channel_id`) REFERENCES `channels` (`id`) ON DELETE CASCADE ON UPDATE CASCADE,
  CONSTRAINT `subscriptions_user_FK` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON DELETE CASCADE ON UPDATE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE `videos` (
  `id` char(26) CHARACTER NOT NULL,
  `channel_id` char(26) CHARACTER DEFAULT NULL,
  `title` varchar(100) CHARACTER NOT NULL,
  `description` longtext CHARACTER NOT NULL DEFAULT '',
  `width` int(11) NOT NULL DEFAULT 0,
  `height` int(11) NOT NULL DEFAULT 0,
  `frame_rate` int(11) NOT NULL DEFAULT 0,
  `duration` float NOT NULL DEFAULT 0,
  `status` enum('ACTIVE','ENCODING','INACTIVE') CHARACTER NOT NULL DEFAULT 'ENCODING',
  `likes` bigint(20) unsigned NOT NULL DEFAULT 0,
  `dislikes` bigint(20) unsigned NOT NULL DEFAULT 0,
  `post_started_at` datetime NOT NULL DEFAULT current_timestamp(),
  `posted_at` datetime DEFAULT NULL,
  `updated_at` datetime NOT NULL DEFAULT current_timestamp(),
  `deactivated_at` datetime DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `videos_FK` (`channel_id`),
  KEY `videos_post_started_at_IDX` (`post_started_at`) USING BTREE,
  KEY `videos_posted_at_IDX` (`posted_at`) USING BTREE,
  KEY `videos_deactivated_at_IDX` (`deactivated_at`) USING BTREE,
  KEY `videos_status_IDX` (`status`) USING BTREE,
  CONSTRAINT `videos_FK` FOREIGN KEY (`channel_id`) REFERENCES `channels` (`id`) ON DELETE CASCADE ON UPDATE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE `comments` (
  `id` char(26) CHARACTER NOT NULL,
  `video_id` char(26) CHARACTER DEFAULT NULL,
  `content` longtext CHARACTER NOT NULL,
  `writer_id` char(26) CHARACTER DEFAULT NULL,
  `posted_at` datetime NOT NULL DEFAULT current_timestamp(),
  `deactivated_at` datetime DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `comments_FK_video` (`video_id`),
  KEY `comments_FK_writer` (`writer_id`),
  KEY `comments_posted_at_IDX` (`posted_at`) USING BTREE,
  KEY `comments_deactivated_at_IDX` (`deactivated_at`) USING BTREE,
  CONSTRAINT `comments_FK_video` FOREIGN KEY (`video_id`) REFERENCES `videos` (`id`) ON DELETE CASCADE ON UPDATE CASCADE,
  CONSTRAINT `comments_FK_writer` FOREIGN KEY (`writer_id`) REFERENCES `users` (`id`) ON DELETE CASCADE ON UPDATE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE `expressions` (
  `video_id` char(26) CHARACTER NOT NULL,
  `user_id` char(26) CHARACTER NOT NULL,
  `type` enum('LIKE','DISLIKE') CHARACTER NOT NULL,
  PRIMARY KEY (`video_id`,`user_id`),
  KEY `expressions_user_FK` (`user_id`),
  CONSTRAINT `expressions_user_FK` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON DELETE CASCADE ON UPDATE CASCADE,
  CONSTRAINT `expressions_video_FK` FOREIGN KEY (`video_id`) REFERENCES `videos` (`id`) ON DELETE CASCADE ON UPDATE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

### Elasticsearch 셋업
MyStream API는 채널과 동영상 두 종류의 문서를 저장하기 위한 두 개의 Elasticsearch 인덱스를 필요로 합니다.

각 문서의 구조는 다음과 같습니다.
* 채널 문서
  |필드|데이터 타입|설명|
  |-|-|-|
  |name|text|채널 이름|
  |description|text|채널 설명|
  |updated_at|date|최근 정보 수정 일시|
* 동영상 문서
  |필드|데이터 타입|설명|
  |-|-|-|
  |title|text|동영상 제목|
  |description|text|동영상 설명|
  |updated_at|date|최근 정보 수정 일시|

이를 바탕으로 작성한 동영상 인덱스 생성 요청의 예는 다음과 같습니다.
```
PUT localhost:9200/videos
```
```json
{
  "mappings": {
    "properties": {
      "updated_at": {
        "type": "date"
      }
    }
  },
  "settings": {
    "analysis": {
      "analyzer": {
        "default": {
          "type": "custom",
          "tokenizer": "nori_tokenizer"
        }
      }
    }
  }
}
```

채널 인덱스의 경우에도 마찬가지로 생성할 수 있습니다. 위 요청에서는 한글 형태소 분석기 nori_tokenizer를 사용했지만 한글 형태소 분석을 지원할 필요가 없을 경우에는 `settings` object를 삭제해도 됩니다.

### 설정 파일 작성
API 서버가 실행될 Working Directory에 설정 파일 `config.json`을 생성하고 값을 설정합니다. 다음은 설정 파일의 예제입니다.

```json
{
	"listen": ":8080",
	"database": {
		"host": "localhost:3306",
		"user": "root",
		"password": "1234",
		"name": "mystream"
	},
	"redis": {
		"addr": "localhost:6379",
		"password": "",
		"database": 0
	},
	"elasticsearch": {
		"url": "http://localhost:9200",
		"video_index": "videos",
		"channel_index": "channels"
	},
	"auth_sign_key": "adsa!cs231!sX@d",
	"upload_sign_key": "da!cjxZX!&*dc31",
  "allow_user_channel": true,
	"storages": {
		"video": {
			"type": "s3",
			"bucket": "mystream.videos"
		},
		"image": {
			"type": "custom",
			"command": ["cp", "-r", "${src}", "/home/user/images/${dst}"]
		}
	},
	"thumbnail": {
		"width": 854,
		"height": 480,
		"quality": 70
	},
	"user_picture": [
		{
			"width": 1024,
			"height": 1024,
			"quality": 70
		},
		{
			"width": 512,
			"height": 512,
			"quality": 70
		}
	],
	"channel_picture": [
		{
			"width": 1024,
			"height": 1024,
			"quality": 70
		},
		{
			"width": 512,
			"height": 512,
			"quality": 70
		}
	],
	"websocket": {
		"enabled": true,
		"ping_interval": 10000,
		"pong_timeout": 15000
	},
	"subscription_bonus": 259200
}
```

설정 파일의 각 필드에 대한 설명은 다음과 같습니다.

* `listen` - 서버의 listen address. 필수
* `database` - 데이터베이스 설정. 필수
    * `host` - 데이터베이스 host address
    * `user` - 데이터베이스 사용자 이름
    * `password` - 데이터베이스 사용자 암호
    * `name` - 데이터베이스 이름
* `redis` - redis 스토리지에 대한 설정. 로드 밸런싱 등 클러스터 구성 시 필수.
    * `addr` - redis 스토리지 주소
    * `password` - redis 스토리지 암호
    * `database` - redis 스토리지 데이터베이스 번호
* `elasticsearch` - elasticsearch 검색 엔진 설정. 필수
    * `url` - elasticsearch API 주소
    * `video_index` - 동영상 문서를 저장할 인덱스 이름
    * `channel_index` - 채널 문서를 저장할 인덱스 이름
* `auth_sign_key` - 사용자 인증에 사용할 JWT sign key
* `upload_sign_key` - 인코더에 영상 전송 시 사용할 JWT sign key. MyStream Encoder 설정의 `upload_sign_key`와 동일해야합니다.
* `allow_user_channel` - 사용자의 채널 생성 허용 여부
* `storage` - 저장소 설정. 필수
    * `video` - 동영상 저장소, 필수
    * `image` - 이미지 저장소, 필수
    * `video`, `image` 공통
        * `type` - 저장소 유형. `s3` 또는 `custom`
        * `bucket` - S3 버킷 이름. 저장소 유형이 `s3`일 경우 필수
        * `aws_endpoint` - 사용자 지정 AWS 엔드포인트.
        * `command` - 저장 명령어 지정. `${src}`는 파일의 상대 경로, `${dst}`는 저장할 상대 경로. 저장소 유형이 `custom`일 경우 필수
* `thumbnail` - 썸네일 이미지 설정. 필수
    * `width` - 가로 크기(px)
    * `height` - 세로 크기(px)
    * `quality` - JPEG 압축 퀄리티(1~100)
* `user_picture` - 사용자 프로필 사진 저장 옵션 목록. 1개 이상 필수
    * `width` - 가로 크기(px)
    * `height` - 세로 크기(px)
    * `quality` - JPEG 압축 퀄리티(1~100)
* `channel_picture` - 채널 프로필 사진 저장 옵션 목록. 1개 이상 필수
    * `width` - 가로 크기(px)
    * `height` - 세로 크기(px)
    * `quality` - JPEG 압축 퀄리티(1~100)
* `websocket` - 알림 기능을 위한 WebSocket 설정. 필수
    * `enabled` - 활성화 여부. `true`로 설정한 노드들만 WebSocket 서버로 사용해야 합니다.
* `subscription_bonus` - 구독 영상 우선순위 가산점. 추천 영상에서 구독한 채널의 영상은 입력한 시간(초)만큼 더 최신의 영상과 동일한 우선순위로 표시됩니다.

### AWS 저장소 설정
동영상 혹은 이미지 저장소로 AWS S3를 사용하는 경우, aws 디렉토리의 `config`, `credential` 파일에 AWS 설정을 작성해야합니다. 다음 예를 참고하세요.

**config**
```ini
[default]
region = ap-northeast-2
```

**credential**
```ini
[default]
aws_access_key_id = [계정 Access Key]
aws_secret_access_key = [계정 Secret Key]
```

### 실행
API 서버의 컴파일된 바이너리를 통해 실행하는 경우 바이너리를 직접 실행합니다.
```console
$ ./mystream-api
```

또는 소스 코드로부터 즉석에서 컴파일하여 실행하려 하는 경우 다음 명령어를 통해 실행합니다. 이 경우 [Go](https://golang.org/dl/)가 설치되어 있어야 합니다.
```console
$ go run .
```

### 빌드
소스 코드를 빌드하여 바이너리를 생성하려면 다음 명령어를 실행합니다.
```console
$ go build
```

## API 문서
API 명세 문서는 [위키](https://github.com/awebow/MyStream-API/wiki)를 참조해주세요.

## Troubleshooting
### Reverse Proxy 사용 시 WebSocket 연결이 되지 않는 경우
`Nginx`나 `Apache` 등의 웹 서버 프로그램을 통해 MyStream Encoder에 Reverse Proxy를 적용하는 경우 `Upgrade`와 `Connection`를 원본 요청과 동일하게 전달해줘야합니다.

다음은 `Nginx`를 사용하는 경우의 설정의 예입니다.
```
server {
        listen 443 ssl;
        server_name api.mystream.mshnet.xyz;
        ssl_certificate /etc/letsencrypt/live/api.mystream.mshnet.xyz/fullchain.pem;
        ssl_certificate_key /etc/letsencrypt/live/api.mystream.mshnet.xyz/privkey.pem;
        include /etc/letsencrypt/options-ssl-nginx.conf;
        ssl_dhparam /etc/letsencrypt/ssl-dhparams.pem;

        location / {
                proxy_set_header Host $host;
                proxy_pass http://127.0.0.1:8080;
        }

        location /ws {
                proxy_set_header Upgrade $http_upgrade;
                proxy_set_header Host $host;
                proxy_set_header Connection "Upgrade";
                proxy_pass http://127.0.0.1:8080;
        }

}
```