# 钉钉机器人

钉钉自定义机器人支持文本 (text)、链接 (link)、markdown、ActionCard、FeedCard消息类型

[官方文档](https://open.dingtalk.com/document/robots/custom-robot-access)

## 调用频率限制

> 每个机器人每分钟最多发送20条消息到群里，如果超过20条，会限流10分钟。  
> **注意**：  
> 如果你有大量发消息的场景（譬如系统监控报警）可以将这些信息进行整合，通过markdown消息以摘要的形式发送到群里。

## 如何使用

您可以使用基于环境变量或配置文件的方法来运行本项目。

### 基于环境变量运行

在运行项目之前，请确保您已经获取了钉钉自定义机器人的`access_token`和`secret`。然后，您可以通过以下命令来运行该项目：

```bash
docker run -d --name dingtalk-robot --restart=unless-stopped -p 8080:8080 \
 -e ACCESS_TOKEN="your dingtalk custom robot aaccess_token" \
 -e SECRET="your dingtalk custom robot secret" \
 -e LOG_LEVEL="info" \
 jerryin/dingtalk-robot
```

| 变量名                   | 是否必填 | 说明                                              |
|-------------------------|-------|----------------------------------------------------|
| `ACCESS_TOKEN`          | 是    | 自定义机器人Webhook的`access_token` [自定义机器人官方说明](https://open.dingtalk.com/document/robots/custom-robot-access) |
| `SECRET`                | 是    | 自定义机器人Webhook的加签`secret`                     |
| `LOG_LEVEL`             | 否    | 日志级别，`debug`、`info`、`warning`、`error`。默认：`info`              |

### 基于配置文件运行

您还可以通过配置文件的方式来运行该项目。在运行项目之前，请确保您已经创建了一个配置文件，并将其挂载到Docker容器中。以下是一个示例配置文件：

```yaml
dingtalk:
    access_token:
    secret:
log:
    level: info
```

运行该项目的命令如下：

```bash
docker run -d --name dingtalk-robot --restart=unless-stopped -p 8080:8080 \
 -v path/to/config.yaml:/config/config.yaml \
 jerryin/dingtalk-robot
 ```

## 调用

### 测试用例

```bash
curl -X POST 'http://127.0.0.1:8080/robot/send' \
 -H 'Content-Type: application/json' \
 -d '{
    "text": "简单文本"
}'
```

### 消息类型及数据格式

1. text类型

```json
{
    "msgtype": "text",
    "text": {
        "content": "我就是我, @XXX 是不一样的烟火"
    },
    "at": {
        "atMobiles": [
            "180xxxxxx"
        ],
        "atUserIds": [
            "user123"
        ],
        "isAtAll": false
    }
}
```

| 参数        | 参数类型    | 是否必填 | 说明                                                          |
|-----------|---------|------|-------------------------------------------------------------|
| msgtype   | String  | 是    | 消息类型，此时固定为：`text`。                                            |
| content   | String  | 是    | 消息内容。                                                       |
| atMobiles | Array   | 否    | 被@人的手机号。 **注意**：在`content`里添加@人的手机号，且只有在群内的成员才可被@，非群内成员手机号会被脱敏。  |
| isAtAll   | Boolean | 否    | 是否@所有人。                                                     |

2. link类型

```json
{
    "msgtype": "link",
    "link": {
        "text": "这个即将发布的新版本，创始人xx称它为红树林。而在此之前，每当面临重大升级，产品经理们都会取一个应景的代号，这一次，为什么是红树林",
        "title": "时代的火车向前开",
        "picUrl": "",
        "messageUrl": "https://www.dingtalk.com/s?__biz=MzA4NjMwMTA2Ng==&mid=2650316842&idx=1&sn=60da3ea2b29f1dcc43a7c8e4a7c97a16&scene=2&srcid=09189AnRJEdIiWVaKltFzNTw&from=timeline&isappinstalled=0&key=&ascene=2&uin=&devicetype=android-23&version=26031933&nettype=WIFI"
    }
}
```

| 参数         | 参数类型   | 是否必填 | 说明                |
|------------|--------|------|-------------------|
| msgtype    | String | 是    | 消息类型，此时固定为：`link`。  |
| title      | String | 是    | 消息标题。             |
| text       | String | 是    | 消息内容。如果太长只会部分展示。  |
| messageUrl | String | 是    | 点击消息跳转的URL。        |
| picUrl     | String | 否    | 图片URL。            |

3. markdown类型

```json
{
    "msgtype": "markdown",
    "markdown": {
        "title": "杭州天气",
        "text": "#### 杭州天气 @150XXXXXXXX \n > 9度，西北风1级，空气良89，相对温度73%\n > ![screenshot](https://img.alicdn.com/tfs/TB1NwmBEL9TBuNjy1zbXXXpepXa-2400-1218.png)\n > ###### 10点20分发布 [天气](https://www.dingtalk.com) \n"
    },
    "at": {
        "atMobiles": [
            "150XXXXXXXX"
        ],
        "atUserIds": [
            "user123"
        ],
        "isAtAll": false
    }
}
```

| 参数        | 类型      | 是否必填 | 说明                                                       |
|-----------|---------|------|----------------------------------------------------------|
| msgtype   | String  | 是    | 消息类型，此时固定为：`markdown`。                                     |
| title     | String  | 是    | 首屏会话透出的展示内容。                                             |
| text      | String  | 是    | markdown格式的消息。                                           |
| atMobiles | Array   | 否    | 被@人的手机号。 注意：在`text`内容里要有@人的手机号，只有在群内的成员才可被@，非群内成员手机号会被脱敏。  |
| isAtAll   | Boolean | 否    | 是否@所有人。

目前只支持markdown语法的子集，具体支持的元素如下：

```
标题
# 一级标题
## 二级标题
### 三级标题
#### 四级标题
##### 五级标题
###### 六级标题

引用
> A man who stands for nothing will fall for anything.

文字加粗、斜体
**bold**
*italic*

链接
[this is a link](http://name.com)

图片
![](http://name.com/pic.jpg)

无序列表
- item1
- item2

有序列表
1. item1
2. item2
```

4. 整体跳转ActionCard类型

```json
{
    "msgtype": "actionCard",
    "actionCard": {
        "title": "乔布斯 20 年前想打造一间苹果咖啡厅，而它正是 Apple Store 的前身",
        "text": "![screenshot](https://gw.alicdn.com/tfs/TB1ut3xxbsrBKNjSZFpXXcXhFXa-846-786.png) \n ### 乔布斯 20 年前想打造的苹果咖啡厅 \n Apple Store 的设计正从原来满满的科技感走向生活化，而其生活化的走向其实可以追溯到 20 年前苹果一个建立咖啡馆的计划",
        "btnOrientation": "0",
        "singleTitle": "阅读全文",
        "singleURL": "https://www.dingtalk.com/"
    }
}
```

| 参数             | 类型     | 是否必填 | 说明                      |
|----------------|--------|------|-------------------------|
| msgtype        | String | 是    | 消息类型，此时固定为：`actionCard`。  |
| title          | String | 是    | 首屏会话透出的展示内容。            |
| text           | String | 是    | markdown格式的消息。          |
| singleTitle    | String | 是    | 单个按钮的标题。                |
| singleURL      | String | 是    | 点击消息跳转的URL。             |
| btnOrientation | String | 否    | 0：按钮竖直排列；1：按钮横向排列       |

5. 独立跳转ActionCard类型

```json
{
    "msgtype": "actionCard",
    "actionCard": {
        "title": "我 20 年前想打造一间苹果咖啡厅，而它正是 Apple Store 的前身",
        "text": "![screenshot](https://img.alicdn.com/tfs/TB1NwmBEL9TBuNjy1zbXXXpepXa-2400-1218.png) \n\n #### 乔布斯 20 年前想打造的苹果咖啡厅 \n\n Apple Store 的设计正从原来满满的科技感走向生活化，而其生活化的走向其实可以追溯到 20 年前苹果一个建立咖啡馆的计划",
        "btnOrientation": "0",
        "btns": [
            {
                "title": "内容不错",
                "actionURL": "https://www.dingtalk.com/"
            },
            {
                "title": "不感兴趣",
                "actionURL": "https://www.dingtalk.com/"
            }
        ]
    }
}
```

| 参数             | 类型     | 是否必填 | 说明                   |
|----------------|--------|------|----------------------|
| msgtype        | String | 是    | 此消息类型为固定：`actionCard`。  |
| title          | String | 是    | 首屏会话透出的展示内容。         |
| text           | String | 是    | markdown格式的消息。       |
| btns           | Array  | 是    | 按钮。                  |
| title          | String | 是    | 按钮标题。                |
| actionURL      | String | 是    | 点击按钮触发的URL。          |
| btnOrientation | String | 否    | 0：按钮竖直排列；1：按钮横向排列    |

5. FeedCard类型

```json
{
    "msgtype": "feedCard",
    "feedCard": {
        "links": [
            {
                "title": "时代的火车向前开1",
                "messageURL": "https://www.dingtalk.com/",
                "picURL": "https://img.alicdn.com/tfs/TB1NwmBEL9TBuNjy1zbXXXpepXa-2400-1218.png"
            },
            {
                "title": "时代的火车向前开2",
                "messageURL": "https://www.dingtalk.com/",
                "picURL": "https://img.alicdn.com/tfs/TB1NwmBEL9TBuNjy1zbXXXpepXa-2400-1218.png"
            }
        ]
    }
}
```

| 参数         | 类型     | 是否必填 | 说明                 |
|------------|--------|------|--------------------|
| msgtype    | String | 是    | 此消息类型为固定：`feedCard`。  |
| title      | String | 是    | 单条信息文本。            |
| messageURL | String | 是    | 点击单条信息到跳转链接。       |
| picURL     | String | 是    | 单条信息后面图片的URL。      |
