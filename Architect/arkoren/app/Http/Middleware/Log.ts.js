import { Middleware } from '../../../../framework/http.ts';
export class Log extends Middleware {
    before(request) {
        console.log('Log before...');
    }
    after(request, response) {
        console.log('Log after...');
    }
}
//# sourceMappingURL=Log.js.map