// sub_store\init.js

// 接收 Go 层注入的常量字面量
const $loon = __LOON_JSON_OBJ__;
const $script = __SCRIPT_JSON_OBJ__;
var $request = {};
var $argument = "";

const $persistentStore = {
    read: function (key) {
        let val = __ps_read(key);
        return val === undefined ? null : val;
    },
    write: function (val, key) {
        __ps_write(val, key);
        return true;
    }
};

const $notification = {
    post: function (title, subtitle, content) {
        __notify_post(title, subtitle, content);
    }
};

// 智能日志防洪堤，阻断重复 Error/Warn 跨越 CGO 疯狂损耗性能
const __log_cache = {};
function __throttled_log(level, ...args) {
    let msg = args.map(String).join(" ");
    let key = level + ":" + msg;
    __log_cache[key] = (__log_cache[key] || 0) + 1;

    if (__log_cache[key] <= 3) {
        __console_log(level, msg);
    } else if (__log_cache[key] === 4) {
        __console_log(level, msg + " (⚠️ 该日志重复出现，已折叠隐藏...)");
    }
}

const console = {
    log: function (...args) { __throttled_log("log", ...args); },
    info: function (...args) { __throttled_log("info", ...args); },
    warn: function (...args) { __throttled_log("warn", ...args); },
    error: function (...args) { __throttled_log("error", ...args); }
};

// JS 侧的无损二进制/Base64转换器
const _b64chars = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/';
const _b64tab = new Uint8Array(256);
for (let i = 0; i < _b64chars.length; i++) _b64tab[_b64chars.charCodeAt(i)] = i;

function __base64_to_uint8array(b64) {
    // 正则必须保留 '='，确保 padding 长度计算不发生 1~2 个字节的残缺丢包！
    b64 = b64.replace(/[^A-Za-z0-9\+\/\=]/g, '');
    let len = b64.length;
    if (len === 0) return new Uint8Array(0);

    let bufLen = (len * 3) / 4;
    if (b64.charAt(len - 1) === '=') bufLen--;
    if (len > 1 && b64.charAt(len - 2) === '=') bufLen--;

    let u8 = new Uint8Array(bufLen);
    let i = 0, j = 0;
    while (i < len) {
        let c1 = _b64tab[b64.charCodeAt(i++)];
        let c2 = _b64tab[b64.charCodeAt(i++)];
        let c3 = _b64tab[b64.charCodeAt(i++)];
        let c4 = _b64tab[b64.charCodeAt(i++)];

        let p = (c1 << 18) | (c2 << 12) | ((c3 !== undefined ? c3 : 0) << 6) | (c4 !== undefined ? c4 : 0);
        u8[j++] = (p >> 16) & 255;
        if (j < bufLen) u8[j++] = (p >> 8) & 255;
        if (j < bufLen) u8[j++] = p & 255;
    }

    // 强制转换为 Buffer，确保内部兼容 Node 原生库的包解析 API
    return typeof Buffer !== 'undefined' ? Buffer.from(u8.buffer, u8.byteOffset, u8.byteLength) : u8;
}

function __uint8array_to_base64(u8) {
    let b64 = "";
    let i = 0, len = u8.length;
    while (i < len) {
        let c1 = u8[i++];
        let c2 = i < len ? u8[i++] : NaN;
        let c3 = i < len ? u8[i++] : NaN;
        let p = (c1 << 16) | ((isNaN(c2) ? 0 : c2) << 8) | (isNaN(c3) ? 0 : c3);
        b64 += _b64chars.charAt((p >> 18) & 63);
        b64 += _b64chars.charAt((p >> 12) & 63);
        b64 += isNaN(c2) ? "=" : _b64chars.charAt((p >> 6) & 63);
        b64 += isNaN(c3) ? "=" : _b64chars.charAt(p & 63);
    }
    return b64;
}

const $httpClient = {};
const __http_callbacks = {};
let __http_req_id = 0;

['get', 'post', 'put', 'patch', 'delete', 'head', 'options'].forEach(method => {
    $httpClient[method] = function (options, callback) {
        let req = typeof options === 'string' ? { url: options } : Object.assign({}, options);

        if (req.body && (req.body instanceof Uint8Array || (typeof Buffer !== 'undefined' && Buffer.isBuffer(req.body)))) {
            req.body = __uint8array_to_base64(req.body);
            req["body-base64"] = true;
        }

        let reqId = "req_" + (++__http_req_id);
        if (callback) { __http_callbacks[reqId] = callback; }
        __go_http_request_async(method.toUpperCase(), JSON.stringify(req), reqId);
    };
});

function __dispatch_http_response(reqId, resMetaJson) {
    let cb = __http_callbacks[reqId];
    if (!cb) return;
    delete __http_callbacks[reqId];

    let res = JSON.parse(resMetaJson);
    let err = res.error !== undefined && res.error !== null ? res.error : null;
    let resp = res.response !== undefined ? res.response : null;

    if (resp && res.reqId !== undefined) {
        let rawBody = __go_http_request_body(res.reqId);
        if (resp.headers && resp.headers["__is_binary__"] === "true") {
            delete resp.headers["__is_binary__"];
            resp.body = __base64_to_uint8array(rawBody);
        } else {
            resp.body = rawBody;
        }
    }
    cb(err, resp, resp ? resp.body : null);
}

// 原生的 UDP DNS 解析能力
const $dns = {
    query: function (options, callback) {
        let reqId = "dns_" + (++__http_req_id);
        if (callback) {
            __http_callbacks[reqId] = callback;
        }
        __go_dns_query_async(JSON.stringify(options), reqId);
    }
};

function __dispatch_dns_response(reqId, resJson) {
    let cb = __http_callbacks[reqId];
    if (!cb) return;
    delete __http_callbacks[reqId];
    let res = JSON.parse(resJson);
    let err = res.error !== undefined && res.error !== null ? { message: res.error } : null;
    let resp = res.response !== undefined ? res.response : null;
    cb(err, resp);
}

// 终极优化：在 JS 侧剥离巨型 Body 绕过 JSON 序列化
const $done = function (val) {
    let bodyStr = undefined;
    if (val && typeof val === 'object') {
        if (val.response && val.response.body !== undefined) {
            bodyStr = typeof val.response.body === 'string' ? val.response.body : JSON.stringify(val.response.body);
            delete val.response.body;
        } else if (val.body !== undefined) {
            bodyStr = typeof val.body === 'string' ? val.body : JSON.stringify(val.body);
            delete val.body;
        }
    }
    let metaJson = val ? JSON.stringify(val) : "{}";
    __done(metaJson, bodyStr || "");
};

// 隔离执行主脚本
function __run_sub_store_script() {
    __SCRIPT_SRC__
}