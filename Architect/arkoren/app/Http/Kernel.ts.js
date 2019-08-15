import { Log } from './Middleware/Log.ts';
import { TrimStrings } from './Middleware/TrimStrings.ts';
import { Kernel as HTTPKernel } from '../../../framework/http.ts';
export class Kernel extends HTTPKernel {
    constructor() {
        super(...arguments);
        this.middleware = [
            Log
        ];
        this.middleware_groups = {
            web: [
                TrimStrings
            ]
        };
    }
}
//# sourceMappingURL=Kernel.js.map