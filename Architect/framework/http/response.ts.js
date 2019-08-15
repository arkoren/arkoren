import { Status } from './status.ts';
export function response(res, status = Status.OK) {
    if (res instanceof HTTPResponse) {
        return res;
    }
    let response;
    switch (typeof res) {
        case 'string': {
            response = new HTTPResponse(status, new TextEncoder().encode(res));
            response.header('Content-Type', 'text/html');
            break;
        }
        default: {
            response = new HTTPResponse(status, new TextEncoder().encode(JSON.stringify(res)));
            response.header('Content-Type', 'application/json');
            break;
        }
    }
    return response;
}
export class HTTPResponse {
    constructor(status, body = new Uint8Array, headers = new Headers, version = 1.1) {
        this.http_status = status;
        this.body = body;
        this.headers = headers;
        this.version = version;
    }
    header(key, value, replace = true) {
        const header = this.headers.get(key);
        if (!header || (header && replace)) {
            this.headers.set(key, value);
        }
        return this;
    }
    getHeaders() {
        return this.headers;
    }
    content() {
        return this.body;
    }
    status() {
        return this.http_status;
    }
}
//# sourceMappingURL=response.js.map