export var Method;
(function (Method) {
    Method["Get"] = "GET";
    Method["Head"] = "HEAD";
    Method["Post"] = "POST";
    Method["Put"] = "PUT";
    Method["Delete"] = "DELETE";
    Method["Connect"] = "CONNECT";
    Method["Options"] = "OPTIONS";
    Method["Trace"] = "TRACE";
    Method["Patch"] = "PATCH";
})(Method || (Method = {}));
export class HTTPRequest {
    constructor(method, url, body = new Uint8Array, headers = new Headers, version = 1.1) {
        this.input_params = {};
        this.query_params = {};
        this.http_method = method;
        this.url = url;
        this.body = body;
        this.headers = headers;
        this.version = version;
    }
    isJson() {
        const [header] = this.header('Content-Type');
        return header.includes('application/json')
            || header.includes('application/ld+json');
    }
    acceptsAnyContentType() {
        return this.accept('*/*', true)
            || this.accept('*', true);
    }
    acceptsJson() {
        return this.accept('application/json')
            || this.accept('application/ld+json');
    }
    acceptsHtml() {
        return this.accept('text/html');
    }
    accept(type, strict = false) {
        const [header] = this.header('Accept');
        if (strict) {
            return header.includes(type);
        }
        return header.includes(type)
            || header.includes('*/*')
            || header.includes('*');
    }
    hasHeader(key) {
        return this.headers.has(key);
    }
    header(key, def) {
        if (!key) {
            const result = [];
            for (const [h] of this.headers) {
                result.push(h);
            }
            return result;
        }
        const header = this.headers.get(key);
        if (!header) {
            return def ? def : [''];
        }
        return [header];
    }
    method() {
        return this.http_method;
    }
    path() {
        return this.url.pathname;
    }
}
//# sourceMappingURL=request.js.map