import { TrimStrings as BaseTrimStrings } from '../../../../framework/middlewares.ts';
export class TrimStrings extends BaseTrimStrings {
    constructor() {
        super(...arguments);
        this.except = [
            'password',
            'password_confirmation',
        ];
    }
}
//# sourceMappingURL=TrimStrings.js.map