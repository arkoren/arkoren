export class NotFound extends Error {
    constructor(message = '404 - Not Found') {
        super(message);
        Object.setPrototypeOf(this, NotFound.prototype);
    }
}
//# sourceMappingURL=errors.js.map