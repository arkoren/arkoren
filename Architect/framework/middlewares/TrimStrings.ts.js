import { Middleware } from '../http.ts';
export class TrimStrings extends Middleware {
    constructor() {
        super(...arguments);
        this.except = [];
    }
    before(request) {
    }
    after(request, response) {
    }
}
//# sourceMappingURL=TrimStrings.js.map